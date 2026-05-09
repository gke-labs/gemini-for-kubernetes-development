// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import React, { useState, useEffect } from 'react';

function Settings({ onBack }) {
    const [githubPat, setGithubPat] = useState('');
    const [geminiKey, setGeminiKey] = useState('');
    const [anthropicKey, setAnthropicKey] = useState('');
    const [geminiProjectID, setGeminiProjectID] = useState('');
    const [status, setStatus] = useState({ 
        github_pat_set: false, 
        gemini_api_key_set: false, 
        anthropic_api_key_set: false,
        gemini_project_id: '' 
    });
    const [isLoading, setIsLoading] = useState(true);
    const [message, setMessage] = useState({ text: '', type: '' }); // type: 'success' or 'error'
    const [versionInfo, setVersionInfo] = useState({ version: '...', commit: '...' });
    const [authStatus, setAuthStatus] = useState(null);
    const [targetNamespace, setTargetNamespace] = useState('');
    const [quotaUsage, setQuotaUsage] = useState(null);
    const [loadingQuota, setLoadingQuota] = useState(false);
    const [quotaError, setQuotaError] = useState(null);

    useEffect(() => {
        fetch('/api/settings')
            .then(res => res.json())
            .then(data => {
                setStatus(data);
                if (data.gemini_project_id) {
                    setGeminiProjectID(data.gemini_project_id);
                }
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
        if (geminiProjectID !== status.gemini_project_id) {
            payload.gemini_project_id = geminiProjectID.trim();
        }

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
                fetch('/api/settings').then(r => r.json()).then(data => {
                    setStatus(data);
                    if (data.gemini_project_id) setGeminiProjectID(data.gemini_project_id);
                });
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

    const handleCheckQuota = () => {
        setLoadingQuota(true);
        setQuotaError(null);
        setQuotaUsage(null);
        fetch('/api/quota')
            .then(async res => {
                if (!res.ok) {
                    const data = await res.json();
                    throw new Error(data.error || 'Failed to check quota');
                }
                return res.json();
            })
            .then(data => {
                setQuotaUsage(data);
                setLoadingQuota(false);
            })
            .catch(err => {
                setQuotaError(err.message);
                setLoadingQuota(false);
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
                    <p style={{ fontSize: '0.9rem', marginTop: '5px' }}>
                        Required for Claude-powered features. 
                    </p>
                </div>

                <div className="form-group">
                    <label htmlFor="geminiProjectID">Google Cloud Project ID:</label>
                    <div className="input-status-wrapper">
                        <input
                            type="text"
                            id="geminiProjectID"
                            value={geminiProjectID}
                            onChange={(e) => setGeminiProjectID(e.target.value)}
                            placeholder="e.g. my-gcp-project-id"
                        />
                    </div>
                    <small>Required for checking quota usage.</small>
                </div>

                <div className="form-actions">
                    <button type="submit" className="btn btn-submit">Save Settings</button>
                    <button type="button" className="btn" onClick={onBack}>Back to Dashboard</button>
                </div>
            </form>

            <div className="quota-section" style={{marginTop: '30px', borderTop: '1px solid #ccc', paddingTop: '20px'}}>
                <h3>Quota Usage</h3>
                <p>Check your Gemini API quota usage (requests per day).</p>
                <button className="btn" onClick={handleCheckQuota} disabled={loadingQuota}>
                    {loadingQuota ? 'Checking...' : 'Check Quota'}
                </button>
                
                {quotaError && <div className="message error" style={{marginTop: '10px'}}>{quotaError}</div>}
                
                {quotaUsage && (
                    <div className="quota-results" style={{marginTop: '15px'}}>
                        {quotaUsage.length === 0 ? (
                            <p>No quota usage found for the configured project/timeframe.</p>
                        ) : (
                            <table className="quota-table" style={{width: '100%', borderCollapse: 'collapse'}}>
                                <thead>
                                    <tr style={{textAlign: 'left', borderBottom: '1px solid #ddd'}}>
                                        <th style={{padding: '8px'}}>Model</th>
                                        <th style={{padding: '8px'}}>Usage (Requests)</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {quotaUsage.map((usage, index) => (
                                        <tr key={index} style={{borderBottom: '1px solid #eee'}}>
                                            <td style={{padding: '8px'}}>{usage.model}</td>
                                            <td style={{padding: '8px'}}>{usage.total}</td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        )}
                        <p style={{fontSize: '0.9em', color: '#666', marginTop: '10px'}}>
                            Note: Quota usage resets at midnight Pacific Time.
                        </p>
                    </div>
                )}
            </div>
            
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
