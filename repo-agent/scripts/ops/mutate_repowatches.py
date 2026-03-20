#!/usr/bin/env python3
import argparse
import copy
import difflib
import json
import subprocess
import sys
import tempfile

def run_command(command):
    try:
        result = subprocess.run(command, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        return result.stdout
    except subprocess.CalledProcessError as e:
        print(f"Error executing command: {' '.join(command)}")
        print(f"Stderr: {e.stderr}")
        sys.exit(1)

def get_repowatches():
    print("Fetching all RepoWatches...")
    try:
        output = run_command(["kubectl", "get", "repowatches", "-A", "-o", "json"])
        return json.loads(output)
    except Exception as e:
        print(f"Failed to get repowatches: {e}")
        sys.exit(1)

def migrate_devcontainer_to_image(repowatch):
    """
    Removes "devcontainerConfigRef": "devcontainer-json" and adds 
    "image": "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest"
    in:
    - .spec.dev
    - .spec.review
    - .spec.issueHandlers[]
    
    If "image" is missing, it is added regardless of whether "devcontainerConfigRef" was present.
    """
    changed = False
    spec = repowatch.get("spec", {})
    target_image = "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest"
    
    # Check spec.dev
    if "dev" in spec and isinstance(spec["dev"], dict):
        if spec["dev"].get("devcontainerConfigRef") == "devcontainer-json":
             print(f"  Removing devcontainerConfigRef from spec.dev")
             del spec["dev"]["devcontainerConfigRef"]
             changed = True
        
        if "image" not in spec["dev"]:
            print(f"  Adding image to spec.dev")
            spec["dev"]["image"] = target_image
            changed = True

    # Check spec.review
    if "review" in spec and isinstance(spec["review"], dict):
        if spec["review"].get("devcontainerConfigRef") == "devcontainer-json":
             print(f"  Removing devcontainerConfigRef from spec.review")
             del spec["review"]["devcontainerConfigRef"]
             changed = True
             
        if "image" not in spec["review"]:
            print(f"  Adding image to spec.review")
            spec["review"]["image"] = target_image
            changed = True
             
    # Check spec.issueHandlers
    if "issueHandlers" in spec and isinstance(spec["issueHandlers"], list):
        for i, handler in enumerate(spec["issueHandlers"]):
            if isinstance(handler, dict):
                if handler.get("devcontainerConfigRef") == "devcontainer-json":
                    print(f"  Removing devcontainerConfigRef from spec.issueHandlers[{i}]")
                    del handler["devcontainerConfigRef"]
                    changed = True
                
                if "image" not in handler:
                    print(f"  Adding image to spec.issueHandlers[{i}]")
                    handler["image"] = target_image
                    changed = True
                
    return changed

def fix_issues_spec(repowatch):
    """
    Resets .spec.issue to the new format.
    """
    spec = repowatch.get("spec", {})
    
    new_issue_spec = {
      "handlers": [
        {
          "labels": [
            "repo-agent"
          ],
          "name": "fix",
          "prompt": "Fix this issue\n",
          "taskType": "fix-issue"
        }
      ],
      "image": "ghcr.io/gke-labs/gemini-for-kubernetes-development/generic-golang:latest",
      "issueShutdownAfterMinutes": 0,
      "llm": {
        "apiKeySecretRef": "gemini-vscode-tokens",
        "provider": "gemini-cli"
      },
      "maxActiveSandboxes": 6,
      "maxSandboxes": 6,
      "robotAccount": "codebot-robot"
    }

    # Check if current spec.issue is different
    current_issue_spec = spec.get("issue")
    
    if current_issue_spec == new_issue_spec:
         return False
    
    print(f"  Updating spec.issue")
    spec["issue"] = new_issue_spec
    return True

def disable_dev_sandboxes(repowatch):
    """
    Sets maxActiveSandboxes and maxSandboxes to 0 in .spec.dev
    """
    changed = False
    spec = repowatch.get("spec", {})
    if "dev" in spec and isinstance(spec["dev"], dict):
        if spec["dev"].get("maxActiveSandboxes") != 0:
            print(f"  Setting spec.dev.maxActiveSandboxes to 0")
            spec["dev"]["maxActiveSandboxes"] = 0
            changed = True
        
        if spec["dev"].get("maxSandboxes") != 0:
            print(f"  Setting spec.dev.maxSandboxes to 0")
            spec["dev"]["maxSandboxes"] = 0
            changed = True
    return changed

def inject_issue_model_list(repowatch):
    """
    Injects the model list into .spec.issue.models
    """
    changed = False
    spec = repowatch.get("spec", {})
    if "issue" in spec and isinstance(spec["issue"], dict):
        target_models = [
            "gemini-3-flash-preview",
            "gemini-3.1-pro-preview",
            "gemini-3.1-flash-lite-preview",
            "gemini-2.5-pro",
            "gemini-2.5-flash",
            "gemini-2.5-flash-lite"
        ]
        
        current_models = spec["issue"].get("models")
        if current_models != target_models:
            print(f"  Updating spec.issue.models")
            spec["issue"]["models"] = target_models
            changed = True
            
    return changed

def set_kcc_workspace_disk_size(repowatch):
    """
    Sets spec.dev.workspaceDiskSize, spec.review.workspaceDiskSize, and spec.issue.workspaceDiskSize
    to 20Gi for k8s-config-connector repowatch.
    """
    if repowatch.get("metadata", {}).get("name") != "k8s-config-connector":
        return False

    changed = False
    spec = repowatch.get("spec", {})
    target_size = "20Gi"

    for section in ["dev", "review", "issue"]:
        if section in spec and isinstance(spec[section], dict):
            if spec[section].get("workspaceDiskSize") != target_size:
                print(f"  Setting spec.{section}.workspaceDiskSize to {target_size}")
                spec[section]["workspaceDiskSize"] = target_size
                changed = True

    return changed

def apply_changes(repowatch):
    namespace = repowatch["metadata"]["namespace"]
    name = repowatch["metadata"]["name"]
    print(f"Applying changes to RepoWatch {namespace}/{name}...")
    
    # Strip resourceVersion to prevent optimistic locking issues if modified concurrently
    # (though unlikely in this script execution flow, good practice for apply)
    if "resourceVersion" in repowatch["metadata"]:
        del repowatch["metadata"]["resourceVersion"]

    with tempfile.NamedTemporaryFile(mode='w+', suffix=".json") as tf:
        json.dump(repowatch, tf)
        tf.flush()
        run_command(["kubectl", "apply", "-f", tf.name])

def show_diff(original, modified):
    namespace = original["metadata"]["namespace"]
    name = original["metadata"]["name"]
    print(f"--- Dry Run Diff for {namespace}/{name} ---")
    
    original_json = json.dumps(original, indent=2, sort_keys=True).splitlines()
    modified_json = json.dumps(modified, indent=2, sort_keys=True).splitlines()
    
    diff = difflib.unified_diff(
        original_json, 
        modified_json, 
        fromfile=f'original/{namespace}/{name}', 
        tofile=f'modified/{namespace}/{name}', 
        lineterm=''
    )
    
    for line in diff:
        print(line)
    print("-------------------------------------------")

def main():
    parser = argparse.ArgumentParser(description="Mutate RepoWatch resources in the cluster.")
    parser.add_argument("--apply", action="store_true", help="Apply changes to the cluster. Defaults to dry-run if not specified.")
    parser.add_argument("--mutator", type=str, help="Short name of the mutator to run. If not provided, lists available mutators.")
    args = parser.parse_args()

    mutators = {
        "migrate-devcontainer-to-image-012026": migrate_devcontainer_to_image,
        "migrate-issues-taskbased-022026": fix_issues_spec,
        "set-dev-maxcounts-0": disable_dev_sandboxes,
        "inject-issue-model-list-02122026": inject_issue_model_list,
        "set-kcc-workspace-disk-size-20Gi-030126": set_kcc_workspace_disk_size,
    }

    if not args.mutator:
        print("Available mutators:")
        for name in mutators:
            print(f"  {name}")
        return

    if args.mutator not in mutators:
        print(f"Unknown mutator: {args.mutator}")
        print("Available mutators:")
        for name in mutators:
            print(f"  {name}")
        sys.exit(1)

    mutation_func = mutators[args.mutator]

    data = get_repowatches()
    items = data.get("items", [])
    
    print(f"Found {len(items)} RepoWatches.")
    
    for item in items:
        name = item["metadata"]["name"]
        namespace = item["metadata"]["namespace"]
        print(f"Inspecting {namespace}/{name}...")
        
        # Deep copy to preserve original state for diffing or because mutations are in-place
        original_item = copy.deepcopy(item)
        item_changed = mutation_func(item)
        
        if item_changed:
            if args.apply:
                apply_changes(item)
            else:
                show_diff(original_item, item)
        else:
            print(f"No changes needed for {namespace}/{name}.")

if __name__ == "__main__":
    main()