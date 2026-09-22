import React, { useState, useEffect } from 'react';

function Settings({ onBack }) {
    const [githubPat, setGithubPat] = useState('');
    const [geminiKey, setGeminiKey] = useState('');
    const [anthropicKey, setAnthropicKey] = useState('');
    const [gcpProject, setGcpProject] = useState('');
    const [patRef, setPatRef] = useState('');
    const [geminiRef, setGeminiRef] = useState('');
    const [anthropicRef, setAnthropicRef] = useState('');
    const [gcpRegion, setGcpRegion] = useState('');
    const [status, setStatus] = useState({ github_pat_set: false, gemini_api_key_set: false, anthropic_api_key_set: false });
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
                setGcpProject(data.gcp_project || '');
                setGcpRegion(data.gcp_region || '');
                setPatRef(data.github_pat_ref || '');
                setGeminiRef(data.gemini_api_key_ref || '');
                setAnthropicRef(data.anthropic_api_key_ref || '');
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
        if (githubPat.trim()) payload.github_pat = githubPat.trim();
        if (geminiKey.trim()) payload.gemini_api_key = geminiKey.trim();
        if (anthropicKey.trim()) {
            const trimmedAnthropicKey = anthropicKey.trim();
            if (!trimmedAnthropicKey.startsWith('sk-ant-')) {
                setMessage({ text: 'Invalid Anthropic API Key format. It should start with "sk-ant-".', type: 'error' });
                return;
            }
            payload.anthropic_api_key = trimmedAnthropicKey;
        }
        if (gcpProject.trim() !== (status.gcp_project || '')) payload.gcp_project = gcpProject.trim();
        if (gcpRegion.trim() !== (status.gcp_region || '')) payload.gcp_region = gcpRegion.trim();
        if (patRef.trim() !== (status.github_pat_ref || '')) payload.github_pat_ref = patRef.trim();
        if (geminiRef.trim() !== (status.gemini_api_key_ref || '')) payload.gemini_api_key_ref = geminiRef.trim();
        if (anthropicRef.trim() !== (status.anthropic_api_key_ref || '')) payload.anthropic_api_key_ref = anthropicRef.trim();

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
                setAnthropicKey('');
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

    const handleClearGeminiKey = () => {
        if (!window.confirm("Are you sure you want to clear your Gemini API Key?")) return;
        
        fetch('/api/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ gemini_api_key: "" })
        })
        .then(res => {
            if (res.ok) {
                setMessage({ text: 'Gemini API Key cleared.', type: 'success' });
                fetch('/api/settings').then(r => r.json()).then(setStatus);
            } else {
                throw new Error('Failed to clear Gemini API Key');
            }
        })
        .catch(err => setMessage({ text: 'Error clearing Gemini API Key.', type: 'error' }));
    };

    const handleClearAnthropicKey = () => {
        if (!window.confirm("Are you sure you want to clear your Anthropic API Key?")) return;
        
        fetch('/api/settings', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ anthropic_api_key: "" })
        })
        .then(res => {
            if (res.ok) {
                setMessage({ text: 'Anthropic API Key cleared.', type: 'success' });
                fetch('/api/settings').then(r => r.json()).then(setStatus);
            } else {
                throw new Error('Failed to clear Anthropic API Key');
            }
        })
        .catch(err => setMessage({ text: 'Error clearing Anthropic API Key.', type: 'error' }));
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
                    <input
                        type="text"
                        id="patRef"
                        value={patRef}
                        onChange={(e) => setPatRef(e.target.value)}
                        placeholder="or import from GCP Secret Manager: projects/…/secrets/…/versions/latest"
                        style={{ marginTop: '4px', width: '100%', fontSize: '0.85rem' }}
                    />
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
                        {status.gemini_api_key_set && (
                            <button type="button" className="btn btn-delete btn-sm" onClick={handleClearGeminiKey} style={{marginLeft: '10px'}}>Clear</button>
                        )}
                    </div>
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Required for AI-powered reviews and triage. 
                    </p>
                    <input
                        type="text"
                        id="geminiRef"
                        value={geminiRef}
                        onChange={(e) => setGeminiRef(e.target.value)}
                        placeholder="or import from GCP Secret Manager: projects/…/secrets/…/versions/latest"
                        style={{ marginTop: '4px', width: '100%', fontSize: '0.85rem' }}
                    />
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Check your <a href="https://ai.dev/rate-limit" target="_blank" rel="noopener noreferrer">token usage</a>.
                    </p>
                </div>

                <div className="form-group">
                    <label htmlFor="anthropicKey">Anthropic API Key:</label>
                    <div className="input-status-wrapper">
                        <input
                            type="password"
                            id="anthropicKey"
                            value={anthropicKey}
                            onChange={(e) => setAnthropicKey(e.target.value)}
                            placeholder={status.anthropic_api_key_set ? "(Currently set - leave blank to keep)" : "Enter new API Key"}
                        />
                         <span className={`status-badge ${status.anthropic_api_key_set ? 'set' : 'missing'}`}>
                            {status.anthropic_api_key_set ? '✅ Configured' : '⚠️ Not Set'}
                        </span>
                        {status.anthropic_api_key_set && (
                            <button type="button" className="btn btn-delete btn-sm" onClick={handleClearAnthropicKey} style={{marginLeft: '10px'}}>Clear</button>
                        )}
                    </div>
                    <input
                        type="text"
                        id="anthropicRef"
                        value={anthropicRef}
                        onChange={(e) => setAnthropicRef(e.target.value)}
                        placeholder="or import from GCP Secret Manager: projects/…/secrets/…/versions/latest"
                        style={{ marginTop: '4px', width: '100%', fontSize: '0.85rem' }}
                    />
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Required for Claude-powered features. 
                    </p>
                </div>

                {status.gsm_sync_principal && (
                    <div className="form-group">
                        <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                            <strong>Importing from GCP Secret Manager:</strong> paste a secret <em>reference</em> above
                            instead of a value — nothing secret is stored here, rotation is a new version in your
                            project, revocation is removing one IAM binding. Grant our sync principal access to each
                            secret you reference:{' '}
                            <a href="https://github.com/gke-labs/gemini-for-kubernetes-development/blob/main/repo-agent/docs/design/gcp-workload-identity.md"
                                target="_blank" rel="noopener noreferrer">How this works ↗</a>
                        </p>
                        <pre style={{ fontSize: '0.8rem', background: 'var(--bg-secondary)', padding: '8px',
                            borderRadius: '6px', overflowX: 'auto', whiteSpace: 'pre-wrap', userSelect: 'all' }}>
                            {status.gsm_grant_example}
                        </pre>
                    </div>
                )}

                <div className="form-group">
                    <label htmlFor="gcpProject">GCP Deployment (bring your own project):</label>
                    <div className="input-status-wrapper">
                        <input
                            type="text"
                            id="gcpProject"
                            value={gcpProject}
                            onChange={(e) => setGcpProject(e.target.value)}
                            placeholder="Project ID (e.g. my-project)"
                        />
                        <input
                            type="text"
                            id="gcpRegion"
                            value={gcpRegion}
                            onChange={(e) => setGcpRegion(e.target.value)}
                            placeholder="Region (e.g. us-central1)"
                            style={{ marginLeft: '10px' }}
                        />
                        <span className={`status-badge ${status.gcp_project ? 'set' : 'missing'}`}>
                            {status.gcp_project ? '✅ Configured' : '⚠️ Not Set'}
                        </span>
                    </div>
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Where runbook deployments land. No credentials are stored — sandbox agents authenticate
                        via Workload Identity, and you grant the principal below access in <em>your</em> project.{' '}
                        <a href="https://github.com/gke-labs/gemini-for-kubernetes-development/blob/main/repo-agent/docs/design/gcp-workload-identity.md"
                            target="_blank" rel="noopener noreferrer">How this works ↗</a>
                    </p>
                    {status.gcp_wi_principal && (
                        <div style={{ marginTop: '8px' }}>
                            <p style={{ fontSize: '0.9rem', margin: '0 0 4px' }}>
                                Grant access by running this in your project (revoke any time by removing the binding):
                            </p>
                            <pre style={{ fontSize: '0.8rem', background: 'var(--bg-secondary)', padding: '8px',
                                borderRadius: '6px', overflowX: 'auto', whiteSpace: 'pre-wrap', userSelect: 'all' }}>
                                {status.gcp_grant_command}
                            </pre>
                        </div>
                    )}
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
