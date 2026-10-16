# A hostname with Google-managed TLS for repo-agent

By default the board is reached at its Gateway's IP, with a self-signed
certificate (`CN=repo-agent`, from `make create-secrets`) that every browser
warns about. This guide gives each cluster a hostname, such as
`https://staging.<domain>`, with a certificate Google issues and renews.

Placeholders: `<project>` and `<region>` are where the cluster runs, `<domain>`
is the domain you register, `<zone>` its Cloud DNS zone, and `<gateway-ip>` a
cluster's Gateway address:

```sh
kubectl get gateway -n repo-agent-system repo-agent-gateway -o jsonpath='{.status.addresses[0].value}'
```

## Choosing an approach

GCP gives a load balancer an IP, not a name, so a hostname needs some domain.

- **A Cloud Run relay**, as grain does: the daemon dials out to a relay on
  Cloud Run, which comes with a `*.run.app` name and managed TLS. But every
  request then goes through a relay process that we would own and operate,
  for a board that already sits behind a Google load balancer.
- **`sslip.io`** (`1-2-3-4.sslip.io` resolves to `1.2.3.4`): no domain to
  buy. But a Google-managed certificate for a name you cannot add DNS
  records to needs load-balancer authorization, which only the global load
  balancer supports. That means another Gateway class and a new IP, and the
  name is tied to the IP.
- **A domain of your own, on the existing regional Gateway, with a
  Certificate Manager certificate (recommended).** Traffic stays on the
  load balancer. A regional Google-managed certificate uses DNS
  authorization, which works with `gke-l7-regional-external-managed`.

## Steps

### 1. Register a domain with its DNS zone

The register command needs the Cloud DNS zone to exist first:

```sh
gcloud dns managed-zones create <zone> --dns-name=<domain>. \
  --description="repo-agent hostnames" --project <project>
gcloud domains registrations search-domains <name> --project <project>   # find a free one
gcloud domains registrations register <domain> --project <project> --cloud-dns-zone=<zone>
```

The registration asks for contact details interactively. The phone number
must include the country code in the form `+1.4155550123`, and the address
line holds only the street, because city, state and postal code have their
own prompts. Choose `redacted-contact-data`. Click the verification email
afterwards, or the domain is suspended after about 15 days. A `.dev` or
`.app` domain is HTTPS-only in every browser (HSTS preload), which suits a
managed certificate.

### 2. Point a name at each Gateway

The GKE Gateway controller reserves each Gateway's external address, and the
address stays as long as the Gateway object exists, so the record does not
go stale. Add one A record per cluster:

```sh
gcloud dns record-sets create staging.<domain>. --type=A --ttl=300 \
  --rrdatas=<gateway-ip> --zone=<zone> --project <project>
```

### 3. Issue the certificate

A single regional wildcard certificate covers every cluster in the region,
and any hostname added later:

```sh
gcloud services enable certificatemanager.googleapis.com --project <project>
gcloud certificate-manager dns-authorizations create <domain-id> \
  --domain=<domain> --type=PER_PROJECT_RECORD --location=<region> --project <project>
gcloud certificate-manager dns-authorizations describe <domain-id> --location=<region> \
  --project <project> --format="value(dnsResourceRecord.name,dnsResourceRecord.data)"
# add the CNAME it prints
gcloud dns record-sets create <record-name> --type=CNAME --ttl=300 \
  --rrdatas=<record-data> --zone=<zone> --project <project>
gcloud certificate-manager certificates create <cert-name> \
  --domains="<domain>,*.<domain>" --dns-authorizations=<domain-id> \
  --location=<region> --project <project>
```

Wait for `managed.state` to become `ACTIVE`, which takes a few minutes:

```sh
gcloud certificate-manager certificates describe <cert-name> --location=<region> --project <project>
```

### 4. Serve it from the Gateway

A regional Gateway takes a Certificate Manager certificate as a TLS option,
not as a `certificateRefs` entry:

```yaml
listeners:
- name: https
  protocol: HTTPS
  port: 443
  tls:
    mode: Terminate
    options:
      networking.gke.io/cert-manager-certs: <cert-name>
```

The deploy does this when `GW_CERT` is set, which the deploy workflows pass
(#1969). With `GW_CERT` set it also drops the plain-HTTP :80 listener (#1970):
an HTTPS-only domain never uses it, and it only served the board unencrypted
on the raw IP. Removing it deletes only the :80 forwarding rule, so the
address stays. Without `GW_CERT` (kind, `make install-repo-agent`) the Gateway
keeps the self-signed `repo-agent-tls` secret and :80, which
`make port-forward` uses.

Once the Gateway has changed, the load balancer takes a few minutes to serve
the new certificate. Check with:

```sh
echo | openssl s_client -connect staging.<domain>:443 -servername staging.<domain> 2>/dev/null \
  | openssl x509 -noout -subject -issuer
```

### 5. GitHub login

The OAuth redirect is built from the request's Host (`pkg/auth/auth.go`), so
nothing in the code changes. Add a callback per hostname to the GitHub OAuth
app: `https://<host>/api/auth/callback`.

### 6. Point overseers at the hostname

An Overseer can link what it posts on GitHub to the agent session that
wrote it: PR descriptions, review-thread and conversation replies, and
reports end with a small `🤖 Agent session` link. The Overseer CRD's
`spec.sessionURL` turns this on (#1971). It's a template with `{sandbox}`
and `{task}`, which factory fills in for each run, so it names the board's
own route for an overseer's session:

```yaml
apiVersion: overseer.gemini.google.com/v1alpha1
kind: Overseer
metadata:
  name: <name>
spec:
  sessionURL: "https://staging.<domain>/#/overseers/<name>/task-session/{sandbox}/{task}"
```

or, on a running cluster:

```sh
kubectl patch overseer <name> --type=merge \
  -p '{"spec":{"sessionURL":"https://staging.<domain>/#/overseers/<name>/task-session/{sandbox}/{task}"}}'
```

- The field must start with `https://`. Unset, nothing is linked.
- It reaches the overseer pod as the env `SESSION_URL`, which `run.sh` writes into the factory config. The pod only picks it up when it is recreated, so run a safe upgrade of the overseer after setting it.
- Links appear on what the overseer posts from then on, not on earlier PRs and comments.
- With `--disclose=false`, factory adds no link, since the link says an agent wrote the text.
- Opening a link needs a GitHub login with access to the overseer: admins, its allowlist, and people with push permission on its repository.

## Operating it

- **Renewal** is automatic.
- **Another cluster or hostname:** add an A record, and `GW_CERT` to that
  cluster's deploy job. The wildcard certificate already covers the name.
- **`repo-agent-tls`** is unused once a cluster serves the managed
  certificate. Keep it for rollback for a while, then delete it. CI does not
  recreate it, because `SKIP_PREQS=true` skips `create-secrets`.
- **Rollback:** point the HTTPS listener back at
  `certificateRefs: [{name: repo-agent-tls}]` by unsetting `GW_CERT`.
- **The raw IP** then shows a name-mismatch warning over HTTPS, and no
  longer answers over HTTP.
- **A hostname change** means updating every Overseer's `sessionURL` too
  (step 6). Links already posted keep the old name.
