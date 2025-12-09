import React, { useState, useEffect } from 'react';
import yaml from 'js-yaml';

function UpdateRepo({ repo, onCancel, onRepoUpdated }) {
    const [yamlContent, setYamlContent] = useState('');
    const [originalYamlContent, setOriginalYamlContent] = useState('');
    const [isLoading, setIsLoading] = useState(false);
    const [error, setError] = useState('');

    useEffect(() => {
        setIsLoading(true);
        fetch(`/api/repos/${repo.name}/yaml`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch repo YAML");
                return res.json();
            })
            .then(data => {
                if (data.yaml) {
                    setYamlContent(data.yaml);
                    setOriginalYamlContent(data.yaml); // Store original YAML content
                }
                setIsLoading(false);
            })
            .catch(err => {
                console.error(err);
                setError(err.message);
                setIsLoading(false);
            });
    }, [repo.name]);

    const handleSubmit = async (e) => {
        e.preventDefault();
        setError('');
        setIsLoading(true);

        try {
            // Validate YAML
            const parsed = yaml.load(yamlContent);
            if (!parsed) throw new Error("YAML is empty or invalid");

            // Extract repoURL from current and original YAML for comparison
            // Note: The YAML content here is the Spec, so repoURL is at the root.
            const currentRepoURL = parsed?.repoURL;
            const originalParsed = yaml.load(originalYamlContent);
            const originalRepoURL = originalParsed?.repoURL;

            if (currentRepoURL && originalRepoURL && currentRepoURL !== originalRepoURL) {
                setError('Repository URL cannot be changed in this interface.');
                setIsLoading(false);
                return;
            }

        } catch (e) {
            setError('Invalid YAML content: ' + e.message);
            setIsLoading(false);
            return;
        }

        fetch(`/api/repos/${repo.name}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ yaml: yamlContent })
        })
        .then(async (res) => {
            if (!res.ok) {
                const contentType = res.headers.get("content-type");
                if (contentType && contentType.indexOf("application/json") !== -1) {
                    const data = await res.json();
                    throw new Error(data.error || 'Failed to update repository');
                } else {
                    const text = await res.text();
                    throw new Error(text || `Failed to update repository (${res.status})`);
                }
            }
            return res;
        })
        .then(() => {
            setIsLoading(false);
            onRepoUpdated();
        })
        .catch(err => {
            console.error(err);
            setError(err.message);
            setIsLoading(false);
        });
    };

    return (
        <div className="add-repo-container"> {/* Reusing add-repo styles */}
            <h2>Update Repository: {repo.name}</h2>
            
            {error && <div className="message error">{error}</div>}

            <form onSubmit={handleSubmit} className="add-repo-form">
                <div className="form-group">
                    <label htmlFor="repoYaml">RepoWatch Spec (YAML):</label>
                    <textarea
                        id="repoYaml"
                        value={yamlContent}
                        onChange={(e) => setYamlContent(e.target.value)}
                        className="yaml-editor"
                        rows={20}
                        disabled={isLoading}
                        style={{fontFamily: 'monospace', width: '100%', whiteSpace: 'pre'}}
                    />
                </div>

                <div className="form-actions">
                    <button type="submit" className="btn btn-submit" disabled={isLoading}>
                        {isLoading ? 'Updating...' : 'Update Repo'}
                    </button>
                    <button type="button" className="btn" onClick={onCancel} disabled={isLoading}>
                        Cancel
                    </button>
                </div>
            </form>
        </div>
    );
}

export default UpdateRepo;
