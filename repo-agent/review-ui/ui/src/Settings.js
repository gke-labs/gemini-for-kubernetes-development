import React, { useState, useEffect } from 'react';

function Settings({ onBack, showConfirm }) {
    const [githubPat, setGithubPat] = useState('');
    const [geminiKey, setGeminiKey] = useState('');
    const [status, setStatus] = useState({ github_pat_set: false, gemini_api_key_set: false });
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

    const handleSwitchNamespace = async (e) => {
        e.preventDefault();

        if (!targetNamespace) {
            const confirmed = await showConfirm("Switching to empty namespace will reset to your default user namespace. Continue?");
            if (!confirmed) return;
        }

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

    const handleClearPat = async () => {
        const confirmed = await showConfirm("Are you sure you want to clear your manual PAT? The application will fall back to your OAuth login token if available.");
        if (!confirmed) return;
        
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

    if (isLoading) return <div className="settings-container"><p style={{padding: '32px', textAlign: 'center'}}>Loading settings...</p></div>;

    return (
        <div className="settings-container">
            {/* Header with icon */}
            <div style={{padding: '24px 32px', borderBottom: '1px solid var(--border-color)', display: 'flex', alignItems: 'center', justifyContent: 'space-between'}}>
                <div style={{display: 'flex', alignItems: 'center', gap: '12px'}}>
                    <span className="material-symbols-outlined" style={{fontSize: '28px', color: 'var(--color-primary)'}}>settings</span>
                    <h2 style={{margin: 0, fontSize: '20px', fontWeight: 700}}>User Settings</h2>
                </div>
                <button className="header-icon-btn" onClick={onBack} title="Back to Dashboard">
                    <span className="material-symbols-outlined">close</span>
                </button>
            </div>

            <div style={{padding: '32px'}}>
                {message.text && <div className={`message ${message.type}`}>{message.text}</div>}

                {/* GitHub PAT Section */}
                <section style={{marginBottom: '32px'}}>
                    <div style={{display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: '16px'}}>
                        <div style={{display: 'flex', alignItems: 'center', gap: '8px'}}>
                            <h3 style={{margin: 0, fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)'}}>GitHub Personal Access Token (PAT)</h3>
                            {status.manual_pat_set ? (
                                <span className="status-badge set">Manual PAT Configured</span>
                            ) : status.oauth_pat_set ? (
                                <span className="status-badge" style={{backgroundColor: 'rgba(59, 130, 246, 0.1)', color: '#3b82f6', border: '1px solid rgba(59, 130, 246, 0.2)'}}>Using OAuth Login Token</span>
                            ) : status.github_pat_set ? (
                                <span className="status-badge set">Legacy PAT Configured</span>
                            ) : (
                                <span className="status-badge missing">No Token Configured</span>
                            )}
                        </div>
                        {status.manual_pat_set && (
                            <button type="button" onClick={handleClearPat} style={{fontSize: '12px', fontWeight: 500, color: 'var(--text-danger)', background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '4px'}}>
                                <span className="material-symbols-outlined" style={{fontSize: '16px'}}>delete</span>
                                Clear Manual PAT
                            </button>
                        )}
                    </div>
                    <div style={{position: 'relative'}}>
                        <div style={{position: 'absolute', left: '12px', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)', pointerEvents: 'none'}}>
                            <span className="material-symbols-outlined" style={{fontSize: '20px'}}>key</span>
                        </div>
                        <input
                            type="password"
                            id="githubPat"
                            value={githubPat}
                            onChange={(e) => setGithubPat(e.target.value)}
                            placeholder={status.manual_pat_set ? "Enter new PAT to overwrite" : "Enter GitHub Personal Access Token"}
                            style={{width: '100%', paddingLeft: '40px', paddingRight: '12px', paddingTop: '12px', paddingBottom: '12px', border: '1px solid var(--border-color-input)', borderRadius: '8px', fontSize: '14px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)', fontFamily: 'var(--font-ui)', boxSizing: 'border-box'}}
                        />
                    </div>
                    <p style={{fontSize: '12px', color: 'var(--text-muted)', marginTop: '8px'}}>
                        Manual PAT takes precedence over OAuth login.
                        You can generate a <a href="https://github.com/settings/tokens" target="_blank" rel="noopener noreferrer" style={{color: 'var(--color-primary)'}}>GitHub Classic PAT</a> with 'repo' (read/write) permissions.
                        {status.oauth_pat_set && !status.manual_pat_set && " You are currently using your GitHub login session."}
                    </p>
                </section>

                {/* Gemini API Key Section */}
                <section style={{marginBottom: '32px'}}>
                    <div style={{display: 'flex', alignItems: 'center', gap: '8px', marginBottom: '16px'}}>
                        <h3 style={{margin: 0, fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)'}}>Gemini API Key</h3>
                        <span className={`status-badge ${status.gemini_api_key_set ? 'set' : 'missing'}`}>
                            {status.gemini_api_key_set ? 'Configured' : 'Not Set'}
                        </span>
                    </div>
                    <div style={{position: 'relative'}}>
                        <div style={{position: 'absolute', left: '12px', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-muted)', pointerEvents: 'none'}}>
                            <span className="material-symbols-outlined" style={{fontSize: '20px'}}>token</span>
                        </div>
                        <input
                            type="password"
                            id="geminiKey"
                            value={geminiKey}
                            onChange={(e) => setGeminiKey(e.target.value)}
                            placeholder={status.gemini_api_key_set ? "(Currently set - leave blank to keep)" : "Enter new API Key"}
                            style={{width: '100%', paddingLeft: '40px', paddingRight: '12px', paddingTop: '12px', paddingBottom: '12px', border: '1px solid var(--border-color-input)', borderRadius: '8px', fontSize: '14px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)', fontFamily: 'var(--font-ui)', boxSizing: 'border-box'}}
                        />
                    </div>
                    <p style={{fontSize: '12px', color: 'var(--text-muted)', marginTop: '8px'}}>
                        Required for AI-powered reviews and triage.
                        Check your <a href="https://ai.dev/rate-limit" target="_blank" rel="noopener noreferrer" style={{color: 'var(--color-primary)'}}>token usage</a>.
                    </p>
                </section>

                {/* Action Buttons */}
                <div style={{display: 'flex', gap: '12px', paddingTop: '16px', borderTop: '1px solid var(--border-color)'}}>
                    <button type="button" className="btn btn-secondary" onClick={onBack} style={{flex: 1, justifyContent: 'center'}}>
                        <span className="material-symbols-outlined" style={{fontSize: '18px'}}>arrow_back</span>
                        Back
                    </button>
                    <button type="button" className="btn btn-submit" onClick={handleSave} style={{flex: 2, justifyContent: 'center'}}>
                        <span className="material-symbols-outlined" style={{fontSize: '18px'}}>save</span>
                        Save Settings
                    </button>
                </div>
            </div>

            {/* Footer with Admin/About */}
            <footer style={{padding: '24px 32px', backgroundColor: 'var(--bg-secondary)', borderTop: '1px solid var(--border-color)'}}>
                {authStatus && authStatus.isAdmin && (
                    <div style={{marginBottom: '24px', paddingBottom: '24px', borderBottom: '1px solid var(--border-color)'}}>
                        <h4 style={{margin: '0 0 12px', fontSize: '12px', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--text-muted)'}}>Admin: Namespace Switching</h4>
                        <p style={{fontSize: '13px', marginBottom: '8px'}}>Current Namespace: <strong>{authStatus.namespace}</strong></p>
                        <form onSubmit={handleSwitchNamespace} style={{display: 'flex', gap: '8px'}}>
                            <input
                                type="text"
                                id="targetNamespace"
                                value={targetNamespace}
                                onChange={(e) => setTargetNamespace(e.target.value)}
                                placeholder="Enter namespace"
                                style={{flex: 1, padding: '8px 12px', borderRadius: '8px', border: '1px solid var(--border-color-input)', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)', fontSize: '13px', fontFamily: 'var(--font-ui)'}}
                            />
                            <button type="submit" className="btn btn-submit" style={{padding: '8px 16px'}}>Switch</button>
                        </form>
                        <small style={{display: 'block', marginTop: '8px', fontSize: '12px', color: 'var(--text-muted)'}}>Enter the namespace you want to manage. Leave empty to return to your default namespace.</small>
                    </div>
                )}
                <div style={{display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '12px'}}>
                    <div style={{display: 'flex', alignItems: 'center', gap: '16px', fontSize: '11px', fontWeight: 500, textTransform: 'uppercase', letterSpacing: '0.08em', color: 'var(--text-muted)'}}>
                        {authStatus && authStatus.isAdmin && <span>Admin Console</span>}
                        {authStatus && authStatus.isAdmin && <span style={{width: '4px', height: '4px', borderRadius: '50%', backgroundColor: 'var(--border-color)'}}></span>}
                        <span>About</span>
                    </div>
                    <div style={{display: 'flex', alignItems: 'center', gap: '16px', fontFamily: 'var(--font-mono)', fontSize: '10px', color: 'var(--text-muted)'}}>
                        <span style={{display: 'flex', alignItems: 'center', gap: '4px'}}>
                            <span className="material-symbols-outlined" style={{fontSize: '12px'}}>info</span>
                            Version: {versionInfo.version}
                        </span>
                        <span style={{display: 'flex', alignItems: 'center', gap: '4px'}}>
                            <span className="material-symbols-outlined" style={{fontSize: '12px'}}>commit</span>
                            Hash: {versionInfo.commit}
                        </span>
                    </div>
                </div>
            </footer>
        </div>
    );
}

export default Settings;
