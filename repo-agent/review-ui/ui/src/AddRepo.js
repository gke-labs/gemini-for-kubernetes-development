import React, { useState, useEffect } from 'react';
import yaml from 'js-yaml';

function AddRepo({ onCancel, onRepoAdded }) {
    const [url, setUrl] = useState('');
    const [name, setName] = useState('');
    const [yamlMode, setYamlMode] = useState(false);
    const [yamlContent, setYamlContent] = useState('');
    const [isLoading, setIsLoading] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        setIsLoading(true);
        fetch('/api/getRepoWatch')
            .then(res => res.json())
            .then(data => {
                if (data.yaml) {
                    setYamlContent(data.yaml);
                }
                setIsLoading(false);
            })
            .catch(err => {
                console.error(err);
                setError('Failed to load default YAML');
                setIsLoading(false);
            });
    }, []);

    const updateYamlWithInputs = (currentContent, currentUrl, currentName) => {
        try {
            let parsed = yaml.load(currentContent);
            if (!parsed) parsed = {};
            if (!parsed.spec) parsed.spec = {};
            if (!parsed.metadata) parsed.metadata = {};

            parsed.spec.repoURL = currentUrl.trim();
            
            let finalName = currentName.trim();
            // Derive name from URL if not provided
            if (!finalName && currentUrl.trim()) {
                try {
                    const urlParts = new URL(currentUrl.trim()).pathname.split('/');
                    if (urlParts.length >= 3) {
                        finalName = urlParts[2].replace(".git", ""); 
                    }
                } catch (e) {
                    // ignore
                }
            }
            parsed.metadata.name = finalName;

            return yaml.dump(parsed);
        } catch (e) {
            console.error("Error updating YAML", e);
            return currentContent;
        }
    };

    const handleSwitchToYaml = () => {
        // Update YAML with current inputs before switching
        const updatedYaml = updateYamlWithInputs(yamlContent, url, name);
        setYamlContent(updatedYaml);
        setYamlMode(true);
    };

    const handleYamlChange = (e) => {
        const newYaml = e.target.value;
        setYamlContent(newYaml);
        try {
            const parsed = yaml.load(newYaml);
            // We don't strictly sync back to inputs while typing to avoid fighting the user
            // but we could potentially update them if valid. 
            // For now, let's keep the one-way sync or loose sync as implemented previously,
            // or just leave inputs as is.
            // The previous implementation updated inputs:
            if (parsed && parsed.spec && parsed.spec.repoURL) {
                setUrl(parsed.spec.repoURL);
            }
            if (parsed && parsed.metadata && parsed.metadata.name) {
                setName(parsed.metadata.name);
            }
        } catch (e) {
            // Ignore parsing errors while typing
        }
    };

    const handleSubmit = async (e) => {
        e.preventDefault();
        setError('');
        setIsLoading(true);

        let finalYaml = yamlContent;

        if (!yamlMode) {
            finalYaml = updateYamlWithInputs(yamlContent, url, name);
        }

        // Common Validation
        try {
            const parsed = yaml.load(finalYaml);
            if (!parsed) throw new Error("YAML is empty or invalid");

            const repoUrl = parsed.spec?.repoURL || '';
            const repoName = parsed.metadata?.name || '';

            if (!repoUrl.trim()) {
                setError('Repository URL is required.');
                setIsLoading(false);
                return;
            }
            if (!repoUrl.startsWith('https://github.com/')) {
                setError('Repository URL must start with https://github.com/');
                setIsLoading(false);
                return;
            }
            if (!repoName.trim()) {
                 setError('Repository Name is required (or could not be derived from URL).');
                 setIsLoading(false);
                 return;
            }

            // If we are here, validation passed. 
            // Re-dump to ensure we submit clean YAML if we just modified it in memory via updateYamlWithInputs
            // (updateYamlWithInputs already returns string, but good to be sure)
            
        } catch (e) {
            setError('Invalid YAML content: ' + e.message);
            setIsLoading(false);
            return;
        }

        const payload = { yaml: finalYaml };

        fetch('/api/repos', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(async (res) => {
            if (!res.ok) {
                const data = await res.json();
                throw new Error(data.error || 'Failed to add repository');
            }
            return res;
        })
        .then(() => {
            setIsLoading(false);
            onRepoAdded();
        })
        .catch(err => {
            console.error(err);
            const hint = " This often happens for private or restricted repositories if you haven't provided a manual GitHub PAT in 'Settings'.";
            setError(err.message + hint);
            setIsLoading(false);
        });
    };

    return (
        <div className="add-repo-container">
            <h2>Watch New Repository</h2>
            <p>Enter the full GitHub URL of the repository you want to watch for PRs and Issues.</p>
            
            {error && <div className="message error">{error}</div>}

            <form onSubmit={handleSubmit} className="add-repo-form">
                {!yamlMode && (
                    <>
                        <div className="form-group">
                            <label htmlFor="repoUrl">Repository URL:</label>
                            <input
                                type="text"
                                id="repoUrl"
                                value={url}
                                onChange={(e) => setUrl(e.target.value)}
                                placeholder="https://github.com/owner/repo"
                                disabled={isLoading}
                            />
                        </div>

                        <div className="form-group">
                            <label htmlFor="repoName">Name (Optional):</label>
                            <input
                                type="text"
                                id="repoName"
                                value={name}
                                onChange={(e) => setName(e.target.value)}
                                placeholder="Custom name for this watch"
                                disabled={isLoading}
                            />
                            <small>Leave blank to use the repository name.</small>
                        </div>
                    </>
                )}

                {yamlMode && (
                    <div className="form-group">
                        <label htmlFor="repoYaml">RepoWatch YAML:</label>
                        <textarea
                            id="repoYaml"
                            value={yamlContent}
                            onChange={handleYamlChange}
                            className="yaml-editor"
                            rows={15}
                            disabled={isLoading}
                            style={{fontFamily: 'monospace', width: '100%', whiteSpace: 'pre'}}
                        />
                    </div>
                )}

                <div className="form-actions">
                    <button type="submit" className="btn btn-submit" disabled={isLoading}>
                        {isLoading ? 'Adding...' : 'Start Watching'}
                    </button>
                    
                    {!yamlMode ? (
                        <button type="button" className="btn" onClick={handleSwitchToYaml} disabled={isLoading}>
                            Advanced YAML
                        </button>
                    ) : (
                        <button type="button" className="btn" onClick={() => setYamlMode(false)} disabled={isLoading}>
                            Simple Mode
                        </button>
                    )}

                    <button type="button" className="btn" onClick={onCancel} disabled={isLoading}>
                        Cancel
                    </button>
                </div>
            </form>
        </div>
    );
}

export default AddRepo;
