#!/usr/bin/env bash
set -e

# The 'install.sh' script is executed as root by the dev container feature

# Function to install on deb-based systems
install_deb() {
    mkdir -p /etc/apt/keyrings
    curl -fsSL https://us-central1-apt.pkg.dev/doc/repo-signing-key.gpg | gpg --dearmor -o /etc/apt/keyrings/antigravity-repo-key.gpg
    echo "deb [signed-by=/etc/apt/keyrings/antigravity-repo-key.gpg] https://us-central1-apt.pkg.dev/projects/antigravity-auto-updater-dev/ antigravity-debian main" | tee /etc/apt/sources.list.d/antigravity.list > /dev/null
    apt update
    apt install -y antigravity
}

# Function to install on rpm-based systems
install_rpm() {
    tee /etc/yum.repos.d/antigravity.repo << EOL
[antigravity-rpm]
name=Antigravity RPM Repository
baseurl=https://us-central1-yum.pkg.dev/projects/antigravity-auto-updater-dev/antigravity-rpm
enabled=1
gpgcheck=0
EOL
    dnf makecache
    dnf install -y antigravity
}

# Detect OS and install
if [ -f /etc/debian_version ]; then
    echo "Debian-based OS detected. Installing Antigravity..."
    install_deb
elif [ -f /etc/redhat-release ]; then
    echo "Red Hat-based OS detected. Installing Antigravity..."
    install_rpm
elif [ -f /etc/fedora-release ]; then
    echo "Fedora-based OS detected. Installing Antigravity..."
    install_rpm
elif [ -f /etc/SuSE-release ]; then # Fallback for SUSE
    echo "SUSE-based OS detected. Installing Antigravity..."
    install_rpm
else
    echo "Unsupported OS for Antigravity installation. Please install manually."
    exit 1
fi

