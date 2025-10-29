#!/usr/bin/env bash
set -e

if [[ -n $VERSION ]]; then
	CODE_SERVER_INSTALL_ARGS="$CODE_SERVER_INSTALL_ARGS --version=\"$VERSION\""
fi

npm install -g @google/gemini-cli@$VERSION
