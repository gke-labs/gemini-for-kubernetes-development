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
    args = parser.parse_args()

    data = get_repowatches()
    items = data.get("items", [])
    
    print(f"Found {len(items)} RepoWatches.")
    
    # List of mutation functions
    mutations = [
        migrate_devcontainer_to_image,
    ]
    
    for item in items:
        name = item["metadata"]["name"]
        namespace = item["metadata"]["namespace"]
        print(f"Inspecting {namespace}/{name}...")
        
        # Deep copy to preserve original state for diffing or because mutations are in-place
        original_item = copy.deepcopy(item)
        item_changed = False
        
        for mutation in mutations:
            if mutation(item):
                item_changed = True
        
        if item_changed:
            if args.apply:
                apply_changes(item)
            else:
                show_diff(original_item, item)
        else:
            print(f"No changes needed for {namespace}/{name}.")

if __name__ == "__main__":
    main()
