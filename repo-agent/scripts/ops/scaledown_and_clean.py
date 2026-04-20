#!/usr/bin/env python3
import argparse
import subprocess
import time
import sys
import json

def run_command(cmd, shell=False, check=True):
    try:
        if shell and isinstance(cmd, list):
             cmd = ' '.join(cmd)
        
        # print(f"Executing: {cmd}")
        result = subprocess.run(cmd, shell=shell, check=check, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        return result.stdout.strip()
    except subprocess.CalledProcessError as e:
        # Don't exit immediately on some errors (like resource not found during get)
        # But for 'check=True' it will raise.
        if check:
            print(f"Error executing command: {cmd}")
            print(f"Stderr: {e.stderr}")
            raise
        return e.stdout.strip() if e.stdout else ""

def main():
    parser = argparse.ArgumentParser(description="Scale down Repowatch and clean up Sandbox resources.")
    parser.add_argument("--apply", action="store_true", help="Apply changes to the cluster. Defaults to dry-run if not specified.")
    parser.add_argument("--types", type=str, help="Comma-separated list of sandbox types to clean up. Defaults to all types.")
    args = parser.parse_args()

    if not args.apply:
        print("Running in DRY-RUN mode. Use --apply to execute changes.")

    print("--- 1. Scaling down repowatch-controller ---")
    try:
        cmd = ["kubectl", "scale", "statefulset", "repowatch-controller", "-n", "repo-agent-system", "--replicas=0"]
        if args.apply:
            run_command(cmd)
            print("Scaled down repowatch-controller to 0.")
        else:
            print(f"Dry Run: Would execute: {' '.join(cmd)}")
    except Exception as e:
        print(f"Warning: Failed to scale down repowatch-controller: {e}")
        print("Continuing...")

    all_resource_types = ["sandboxes", "sandboxtasks"]
    
    if args.types:
        resource_types = [t.strip() for t in args.types.split(",") if t.strip()]
        cleaning_all = False
    else:
        resource_types = all_resource_types
        cleaning_all = True
    
    print(f"--- 2. Deleting Sandbox Resources ({', '.join(resource_types)}) ---")
    existing_types = []
    # Always check what exists, safe read-only operation
    for r in resource_types:
        try:
            # Check if CRD exists/resource exists by listing (ignoring output)
            run_command(["kubectl", "get", r, "-A"], check=True) 
            existing_types.append(r)
        except:
            print(f"Resource type {r} not found or not accessible.")
            
    if not existing_types:
        print("No sandbox resource types found to delete.")
    else:
        # Delete each type individually
        for r in existing_types:
            cmd = ["kubectl", "delete", r, "--all", "-A", "--wait=false"]
            if args.apply:
                try:
                    run_command(cmd)
                    print(f"Delete command issued for: {r}")
                except Exception as e:
                    print(f"Error issuing delete command for {r}: {e}")
            else:
                print(f"Dry Run: Would execute: {' '.join(cmd)}")

    # Step 3 makes no sense in dry-run if we didn't delete anything, but we can list what is there.
    if args.apply:
        print("--- 3. Waiting for resources to disappear ---")
        timeout = 60
        start_time = time.time()
        
        # We loop and check if they are gone.
        while time.time() - start_time < timeout:
            remaining_count = 0
            for r in existing_types:
                try:
                    out = run_command(["kubectl", "get", r, "-A", "-o", "json"], check=False)
                    if out:
                        data = json.loads(out)
                        items = data.get("items", [])
                        if len(items) > 0:
                            print(f"  {len(items)} {r} remaining...")
                            remaining_count += len(items)
                except:
                    pass
            
            if remaining_count == 0:
                print("All sandbox CRs deleted.")
                break
            
            time.sleep(3)
        
        if time.time() - start_time >= timeout:
            print("Timeout reached. Patching finalizers to force delete...")
            for r in existing_types:
                 try:
                    out = run_command(["kubectl", "get", r, "-A", "-o", "json"], check=False)
                    if not out: continue
                    
                    data = json.loads(out)
                    items = data.get("items", [])
                    
                    for item in items:
                        ns = item["metadata"]["namespace"]
                        name = item["metadata"]["name"]
                        print(f"  Removing finalizers from {r} {ns}/{name}...")
                        # Patch to remove finalizers
                        patch_cmd = ["kubectl", "patch", r, name, "-n", ns, "--type=json", "-p", '[{"op":"remove", "path":"/metadata/finalizers"}]']
                        try:
                            run_command(patch_cmd)
                        except Exception as e:
                            print(f"    Failed to patch {name}: {e}")
                 except Exception as e:
                    print(f"Error during patching loop for {r}: {e}")
    else:
        print("--- 3. (Dry Run) Skipping wait and force-delete phase ---")
        # Optional: list what IS currently there to show what would have been waited on
        for r in existing_types:
            try:
                out = run_command(["kubectl", "get", r, "-A", "-o", "json"], check=False)
                if out:
                    data = json.loads(out)
                    items = data.get("items", [])
                    if items:
                         print(f"Dry Run: Found {len(items)} existing {r} that would be deleted.")
            except:
                pass

    if cleaning_all:
        print("--- 4. Ensuring Pods are deleted ---")
        labels = ["sandbox.gemini.google.com/type", "sandbox"]
        
        for label in labels:
            try:
                out = run_command(["kubectl", "get", "pods", "-A", "-l", label, "--no-headers"], check=False)
                if out:
                    cmd = ["kubectl", "delete", "pods", "-A", "-l", label, "--wait=false"]
                    if args.apply:
                        print(f"Found orphaned pods with label {label}. Deleting...")
                        run_command(cmd)
                        print(f"Delete command issued for orphaned pods with label {label}.")
                    else:
                        print(f"Dry Run: Found orphaned pods with label {label}. Would execute: {' '.join(cmd)}")
                else:
                    print(f"No pods found with label {label}.")
            except Exception as e:
                print(f"Error checking/deleting pods with label {label}: {e}")
    else:
        print("--- 4. Skipping Pod deletion (specific types selected) ---")

    print("--- Done ---")

if __name__ == "__main__":
    main()