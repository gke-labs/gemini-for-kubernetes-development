# Interactive Terminal

The Repo Agent provides a fully functional, interactive terminal directly within the Review UI for both Pull Request and Issue Sandboxes. This allows you to explore the sandbox environment, debug issues, run manual commands, and even edit files without leaving your browser.

## Overview

The terminal provides secure, direct access to the container running your sandbox environment. It supports standard shell commands, interactive text editors (like `vim` or `nano`), and process monitoring tools (like `top`).

Key features include:
- **Direct SSH Access**: Connects securely via Kubernetes `exec` and SSH.
- **Full Interactivity**: Supports ncurses-based applications (e.g., `vim`, `htop`).
- **Resizable Interface**: Drag the bottom edge to resize the terminal window vertically.
- **Clipboard Integration**: Copy and paste text as you would in a native terminal.

## Accessing the Terminal

The terminal is available on any **active** sandbox (indicated by a green status indicator).

1.  Navigate to the **Review Dashboard**.
2.  Locate the card for your **Pull Request** or **Issue**.
3.  Ensure the sandbox status is **Active** (Green).
4.  Click the **Terminal Icon (`>_`)** in the top-right header of the card.

![Terminal Access Icon](../images/terminal-icon-placeholder.png)

*Note: The icon will only appear when the sandbox is fully provisioned and ready.*

## Using the Terminal

Once opened, the terminal panel will expand immediately below the card header. You will be logged in as the default user (usually `root` or `vscode`, depending on the image configuration) in the project's root directory.

### Common Tasks

*   **File Exploration**:
    ```bash
    ls -la
    cd src/
    ```
*   **Git Operations**:
    You can manually run git commands to check status or diffs:
    ```bash
    git status
    git diff
    ```
*   **Editing Files**:
    The terminal supports `vim` for quick edits:
    ```bash
    vim README.md
    ```
    *(Press `i` to insert, `Esc` then `:wq` to save and quit)*

*   **Process Monitoring**:
    Check running processes or resource usage:
    ```bash
    top
    ```
    *(Press `q` to exit)*

### Persistent Sessions & Gemini CLI

*   **Persistent Sessions with `tmux`**:
    To keep your terminal session active even if the browser connection drops, use `tmux`:
    ```bash
    tmux new -s work
    # ... do your work ...
    # Press Ctrl+b then d to detach
    tmux attach -t work
    ```

*   **AI Assistance with `gemini-cli`**:
    Interact with the Gemini model directly from the command line to ask questions or generate code:
    ```bash
    gemini-cli prompt "How do I list all pods in the default namespace?"
    gemini-cli code "Write a Python script to parse JSON from stdin"
    ```

### Resizing the Window

If you need more vertical space, simply click and drag the **bottom-right corner** (or bottom edge) of the terminal window downwards. The terminal content will automatically reflow to fit the new dimensions.

## Troubleshooting

*   **Connection Closed**: If the connection drops (e.g., network interruption), the terminal will display a "Connection closed" message. Refresh the page or close and reopen the terminal to reconnect.
*   **Garbled Text**: If you see strange characters, it might be due to an encoding mismatch. The terminal is configured for UTF-8. Ensure your browser supports it.
*   **Scaling Down**: If the sandbox scales down (status turns yellow/grey) due to inactivity, the terminal session will be terminated. You will need to "Scale Up" the sandbox before accessing the terminal again.

---

*This feature is powered by a WebSocket connection to a custom SSH server running within the sandbox pod, proxied securely through the Repo Agent API.*
