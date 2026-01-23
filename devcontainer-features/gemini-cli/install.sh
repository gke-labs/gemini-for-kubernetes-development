#!/usr/bin/env bash
set -e

if [[ -n $VERSION ]]; then
	CODE_SERVER_INSTALL_ARGS="$CODE_SERVER_INSTALL_ARGS --version=\"$VERSION\""
fi

npm install -g @google/gemini-cli@$VERSION

# Check for and install extensions from extensions.txt
EXTENSIONS_FILE=$(find /workspaces -name "extensions.txt" -type f -print -quit)
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
    echo "No extensions.txt file found in /workspaces, skipping extension installation."
fi

# Check for and install extensions from extensions.txt
EXTENSIONS_FILE=$(find /workspaces -name "extensions.txt" -type f -print -quit)
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
    echo "No extensions.txt file found in /workspaces, skipping extension installation."
fi
