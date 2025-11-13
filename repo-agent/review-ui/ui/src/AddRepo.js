import React, { useState } from 'react';

function AddRepo({ onCancel, onRepoAdded }) {
    const [url, setUrl] = useState('');
    const [name, setName] = useState('');
    const [isLoading, setIsLoading] = useState(false);
    const [error, setError] = useState('');

    const handleSubmit = (e) => {
        e.preventDefault();
        setError('');

        // Basic validation
        if (!url.trim()) {
            setError('Please enter a repository URL.');
            return;
        }
        if (!url.startsWith('https://github.com/')) {
             setError('URL must start with https://github.com/');
             return;
        }

        setIsLoading(true);
        
        const payload = { url: url.trim() };
        if (name.trim()) {
            payload.name = name.trim();
        }

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
            setError(err.message);
            setIsLoading(false);
        });
    };

    return (
        <div className="add-repo-container">
            <h2>Watch New Repository</h2>
            <p>Enter the full GitHub URL of the repository you want to watch for PRs and Issues.</p>
            
            {error && <div className="message error">{error}</div>}

            <form onSubmit={handleSubmit} className="add-repo-form">
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

                <div className="form-actions">
                    <button type="submit" className="btn btn-submit" disabled={isLoading}>
                        {isLoading ? 'Adding...' : 'Start Watching'}
                    </button>
                    <button type="button" className="btn" onClick={onCancel} disabled={isLoading}>
                        Cancel
                    </button>
                </div>
            </form>
        </div>
    );
}

export default AddRepo;