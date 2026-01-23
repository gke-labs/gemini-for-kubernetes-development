#!/usr/bin/env bash
set -e
npm install -g @google/gemini-cli
# Check for and install extensions from extensions.txt
EXTENSIONS_FILE=$(find . -name "extensions.txt" -type f -print -quit)
if [ -n "$EXTENSIONS_FILE" ] && [ -f "$EXTENSIONS_FILE" ]; then
    echo "Found extensions.txt at $EXTENSIONS_FILE, installing extensions..."
    while IFS= read -r line || [ -n "$line" ]; do
        # skip empty lines and comments
        if [ -z "$line" ] || [[ "$line" = \#* ]]; then
            continue
        fi
        echo "Installing extension: $line"
        gemini extensions install $line --auto-update --consent
    done < "$EXTENSIONS_FILE"
    echo "Finished installing extensions from extensions.txt."
else
    echo "No extensions.txt file found, skipping extension installation."
fi
gemini extensions list
