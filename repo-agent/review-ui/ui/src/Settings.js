import React, { useState, useEffect } from 'react';

function Settings({ onBack }) {
    const [githubPat, setGithubPat] = useState('');
    const [geminiKey, setGeminiKey] = useState('');
    const [claudeKey, setClaudeKey] = useState('');
    const [status, setStatus] = useState({ github_pat_set: false, gemini_api_key_set: false, claude_api_key_set: false });
    const [isLoading, setIsLoading] = useState(true);
    const [message, setMessage] = useState({ text: '', type: '' }); // type: 'success' or 'error'
    const [versionInfo, setVersionInfo] = useState({ version: '...', commit: '...' });
    const [authStatus, setAuthStatus] = useState(null);
    const [targetNamespace, setTargetNamespace] = useState('');

    useEffect(() => {
        fetch('/api/settings')
            .then(res => res.json())
            .then(data => {
                setStatus(data);
                setIsLoading(false);
            })
            .catch(err => {
                console.error("Failed to fetch settings status:", err);
                setIsLoading(false);
            });
        
        fetch('/api/version')
            .then(res => res.json())
            .then(data => setVersionInfo(data))
            .catch(err => console.error("Failed to fetch version:", err));

        fetch('/api/auth/status')
            .then(res => res.json())
            .then(data => {
                setAuthStatus(data);
                if (data.namespace) setTargetNamespace(data.namespace);
            })
            .catch(err => console.error("Failed to fetch auth status:", err));
    }, []);

    const handleSave = (e) => {
        e.preventDefault();
        setMessage({ text: 'Saving...', type: 'info' });

        const payload = {};
        if (githubPat) payload.github_pat = githubPat;
        if (geminiKey) payload.gemini_api_key = geminiKey;
        if (claudeKey) payload.claude_api_key = claudeKey;

        if (Object.keys(payload).length === 0) {
             setMessage({ text: 'Nothing to update.', type: 'info' });
             return;
        }

        fetch('/api/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(res => {
            if (res.ok) {
                setMessage({ text: 'Settings updated successfully!', type: 'success' });
                setGithubPat('');
                setGeminiKey('');
                setClaudeKey('');
                // Refresh status
                fetch('/api/settings').then(r => r.json()).then(setStatus);
            } else {
                throw new Error('Failed to update settings');
            }
        })
        .catch(err => {
            console.error(err);
            setMessage({ text: 'Error updating settings.', type: 'error' });
        });
    };

    const handleSwitchNamespace = (e) => {
        e.preventDefault();
        
        // If target namespace is empty, we confirm if they want to reset
        if (!targetNamespace && !window.confirm("Switching to empty namespace will reset to your default user namespace. Continue?")) return;

        fetch('/api/auth/switch-namespace', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ namespace: targetNamespace })
        })
        .then(res => {
            if (res.ok) {
                window.location.reload(); 
            } else {
                res.json().then(data => {
                    setMessage({ text: 'Failed to switch namespace: ' + (data.error || 'Unknown error'), type: 'error' });
                }).catch(() => {
                    setMessage({ text: 'Failed to switch namespace.', type: 'error' });
                });
            }
        })
        .catch(err => {
             console.error(err);
             setMessage({ text: 'Error switching namespace.', type: 'error' });
        });
    };

    const handleClearPat = () => {
        if (!window.confirm("Are you sure you want to clear your manual PAT? The application will fall back to your OAuth login token if available.")) return;
        
        fetch('/api/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ github_pat: "" })
        })
        .then(res => {
            if (res.ok) {
                setMessage({ text: 'Manual PAT cleared.', type: 'success' });
                fetch('/api/settings').then(r => r.json()).then(setStatus);
            } else {
                throw new Error('Failed to clear PAT');
            }
        })
        .catch(err => setMessage({ text: 'Error clearing PAT.', type: 'error' }));
    };

    if (isLoading) return <div className="settings-container"><p>Loading settings...</p></div>;

    return (
        <div className="settings-container">
            <h2>User Settings</h2>
            <p>Configure your personal access tokens. These are stored securely in your private namespace.</p>
            
            {message.text && <div className={`message ${message.type}`}>{message.text}</div>}

            <form onSubmit={handleSave} className="settings-form">
                <div className="form-group">
                    <label htmlFor="githubPat">GitHub Personal Access Token (PAT):</label>
                    <div className="status-info">
                        {status.manual_pat_set ? (
                            <span className="status-badge set">✅ Manual PAT Configured</span>
                        ) : status.oauth_pat_set ? (
                            <span className="status-badge oauth">ℹ️ Using OAuth Login Token</span>
                        ) : status.github_pat_set ? (
                             <span className="status-badge set">✅ Legacy PAT Configured</span>
                        ) : (
                            <span className="status-badge missing">⚠️ No Token Configured</span>
                        )}
                    </div>
                    <div className="input-status-wrapper">
                        <input
                            type="password"
                            id="githubPat"
                            value={githubPat}
                            onChange={(e) => setGithubPat(e.target.value)}
                            placeholder={status.manual_pat_set ? "Enter new PAT to overwrite" : "Enter new Manual PAT"}
                        />
                        {status.manual_pat_set && (
                            <button type="button" className="btn btn-delete btn-sm" onClick={handleClearPat} style={{marginLeft: '10px'}}>Clear Manual PAT</button>
                        )}
                    </div>
                    <small>
                        Manual PAT takes precedence over OAuth login. 
                        You can generate a <a href="https://github.com/settings/tokens" target="_blank" rel="noopener noreferrer">GitHub Classic PAT</a> with 'repo' (read/write) permissions.
                        {status.oauth_pat_set && !status.manual_pat_set && " You are currently using your GitHub login session."}
                    </small>
                </div>

                <div className="form-group">
                    <label htmlFor="geminiKey">Gemini API Key:</label>
                    <div className="input-status-wrapper">
                        <input
                            type="password"
                            id="geminiKey"
                            value={geminiKey}
                            onChange={(e) => setGeminiKey(e.target.value)}
                            placeholder={status.gemini_api_key_set ? "(Currently set - leave blank to keep)" : "Enter new API Key"}
                        />
                         <span className={`status-badge ${status.gemini_api_key_set ? 'set' : 'missing'}`}>
                            {status.gemini_api_key_set ? '✅ Configured' : '⚠️ Not Set'}
                        </span>
                    </div>
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Required for AI-powered reviews and triage. 
                        Check your <a href="https://ai.dev/rate-limit" target="_blank" rel="noopener noreferrer">token usage</a>.
                    </p>
                </div>

                <div className="form-group">
                    <label htmlFor="claudeKey">Claude API Key:</label>
                    <div className="input-status-wrapper">
                        <input
                            type="password"
                            id="claudeKey"
                            value={claudeKey}
                            onChange={(e) => setClaudeKey(e.target.value)}
                            placeholder={status.claude_api_key_set ? "(Currently set - leave blank to keep)" : "Enter new API Key"}
                        />
                         <span className={`status-badge ${status.claude_api_key_set ? 'set' : 'missing'}`}>
                            {status.claude_api_key_set ? '✅ Configured' : '⚠️ Not Set'}
                        </span>
                    </div>
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Required for Claude-powered analysis.
                    </p>
                </div>

                <div className="form-actions">
                    <button type="submit" className="btn btn-submit">Save Settings</button>
                    <button type="button" className="btn" onClick={onBack}>Back to Dashboard</button>
                </div>
            </form>
            
            {authStatus && authStatus.isAdmin && (
                <div className="admin-section" style={{marginTop: '40px', borderTop: '1px solid #eee', paddingTop: '20px'}}>
                    <h3>Admin: Namespace Switching</h3>
                    <p>Current Namespace: <strong>{authStatus.namespace}</strong></p>
                    <form onSubmit={handleSwitchNamespace} className="settings-form">
                         <div className="form-group">
                            <label htmlFor="targetNamespace">Target Namespace:</label>
                            <div className="input-status-wrapper">
                                <input
                                    type="text"
                                    id="targetNamespace"
                                    value={targetNamespace}
                                    onChange={(e) => setTargetNamespace(e.target.value)}
                                    placeholder="Enter namespace"
                                />
                                <button type="submit" className="btn btn-submit" style={{marginLeft: '10px'}}>Switch</button>
                            </div>
                            <small>Enter the namespace you want to manage. Leave empty to return to your default namespace.</small>
                         </div>
                    </form>
                </div>
            )}
            
            <div className="about-section" style={{marginTop: '40px', borderTop: '1px solid #eee', paddingTop: '20px', color: '#666', fontSize: '0.9em'}}>
                <h3 style={{fontSize: '1.1em', marginBottom: '10px'}}>About Repo Agent</h3>
                <p style={{margin: '5px 0'}}><strong>Version:</strong> {versionInfo.version}</p>
                <p style={{margin: '5px 0'}}><strong>Git Commit:</strong> <code style={{background: '#f5f5f5', padding: '2px 5px', borderRadius: '3px'}}>{versionInfo.commit}</code></p>
            </div>
        </div>
    );
}

export default Settings;
