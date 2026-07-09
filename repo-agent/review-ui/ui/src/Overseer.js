import React, { useState, useEffect, useCallback, useRef } from 'react';
import './App.css';
import SandboxTerminal from './Terminal';

const ChevronRight = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" style={{opacity: 0.8}}>
    <path fillRule="evenodd" d="M6.22 3.22a.75.75 0 0 1 1.06 0l4.25 4.25a.75.75 0 0 1 0 1.06l-4.25 4.25a.75.75 0 0 1-1.06-1.06L9.94 8 6.22 4.28a.75.75 0 0 1 0-1.06z"/>
  </svg>
);

const ChevronDown = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" style={{opacity: 0.8}}>
    <path fillRule="evenodd" d="M3.22 6.22a.75.75 0 0 1 1.06 0L8 9.94l3.72-3.72a.75.75 0 1 1 1.06 1.06l-4.25 4.25a.75.75 0 0 1-1.06 0l-4.25-4.25a.75.75 0 0 1 0-1.06z"/>
  </svg>
);

const Overseer = ({ onBack, namespace: userNamespace }) => {
    const [overseers, setOverseers] = useState([]);
    const [error, setError] = useState(null);
    const [activeOverseer, setActiveOverseer] = useState(null);
    const [sandboxes, setSandboxes] = useState([]);
    const [activeSandbox, setActiveSandbox] = useState(null);
    const [tasks, setTasks] = useState([]);
    const [searchFilter, setSearchFilter] = useState('');
    
    const [logs, setLogs] = useState('');
    const [showOverseerLogs, setShowOverseerLogs] = useState(false);
    const [showTerminal, setShowTerminal] = useState(false);
    const [showPodLogs, setShowPodLogs] = useState(false);
    const [taskLogs, setTaskLogs] = useState({});

    const logIntervalRef = useRef(null);

    const fetchOverseers = useCallback(() => {
        fetch('/api/overseers')
            .then(async res => {
                if (!res.ok) {
                    let errMsg = `HTTP error ${res.status}`;
                    try {
                        const errData = await res.json();
                        if (errData && errData.error) errMsg = errData.error;
                    } catch (e) {}
                    throw new Error(errMsg);
                }
                return res.json();
            })
            .then(data => {
                setError(null);
                setOverseers(data || []);
                setActiveOverseer(prev => {
                    if (data && data.length > 0 && !prev) {
                        return data[0];
                    } else if (prev) {
                        const updated = (data || []).find(o => o.metadata?.name === prev.metadata?.name);
                        return updated || prev;
                    }
                    return prev;
                });
            })
            .catch(err => {
                console.error("Failed to fetch overseers:", err);
                setError(err.message);
                setOverseers([]);
            });
    }, []);

    useEffect(() => {
        fetchOverseers();
        const interval = setInterval(fetchOverseers, 15000);
        return () => clearInterval(interval);
    }, [fetchOverseers]);

    const fetchSandboxes = useCallback(() => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch sandboxes");
                return res.json();
            })
            .then(data => {
                setSandboxes(data || []);
                setActiveSandbox(prev => {
                    if (!prev) return null;
                    const updated = (data || []).find(s => s.metadata?.name === prev.metadata?.name);
                    return updated || prev;
                });
            })
            .catch(err => {
                console.error("Failed to fetch sandboxes:", err);
                setSandboxes([]);
            });
    }, [activeOverseer]);

    useEffect(() => {
        fetchSandboxes();
        const interval = setInterval(fetchSandboxes, 5000);
        return () => clearInterval(interval);
    }, [fetchSandboxes]);

    const fetchSandboxTasks = useCallback(() => {
        if (!activeOverseer || !activeSandbox) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/tasks`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch sandbox tasks");
                return res.json();
            })
            .then(data => {
                setTasks(data || []);
            })
            .catch(err => {
                console.error("Failed to fetch sandbox tasks:", err);
                setTasks([]);
            });
    }, [activeOverseer, activeSandbox]);

    useEffect(() => {
        if (activeSandbox) {
            fetchSandboxTasks();
            const interval = setInterval(fetchSandboxTasks, 5000);
            return () => clearInterval(interval);
        } else {
            setTasks([]);
        }
    }, [activeSandbox, fetchSandboxTasks]);

    const fetchLogs = useCallback(() => {
        if (!activeOverseer) return;
        let url = '';
        if (showOverseerLogs) {
            url = `/api/overseers/${activeOverseer.metadata.name}/logs`;
        } else if (activeSandbox && showPodLogs) {
            url = `/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/logs`;
        } else {
            return;
        }

        fetch(url)
            .then(res => res.text())
            .then(data => setLogs(data))
            .catch(err => console.error("Failed to fetch logs:", err));
    }, [activeOverseer, activeSandbox, showOverseerLogs, showPodLogs]);

    useEffect(() => {
        if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        
        if (showOverseerLogs || (activeSandbox && showPodLogs)) {
            fetchLogs();
            logIntervalRef.current = setInterval(fetchLogs, 5000);
        } else {
            setLogs('');
        }

        return () => {
            if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        };
    }, [showOverseerLogs, showPodLogs, activeSandbox, fetchLogs]);

    const handleOverseerClick = (ov) => {
        if (activeOverseer?.metadata.name === ov.metadata.name) {
            setActiveOverseer(null);
            setActiveSandbox(null);
            setShowOverseerLogs(false);
            setShowTerminal(false);
            setShowPodLogs(false);
        } else {
            setActiveOverseer(ov);
            setActiveSandbox(null);
            setShowOverseerLogs(true);
            setShowTerminal(false);
            setShowPodLogs(false);
        }
    };

    const handleSandboxClick = (sb) => {
        setActiveSandbox(sb);
        setShowOverseerLogs(false);
        setShowTerminal(false);
        setShowPodLogs(false);
    };

    const handleDeleteSandbox = (sbName) => {
        if (!activeOverseer) return;
        if (!window.confirm(`Are you sure you want to delete/evict sandbox '${sbName}'?`)) return;

        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${sbName}`, { method: 'DELETE' })
            .then(res => {
                if (res.ok) {
                    if (activeSandbox?.metadata?.name === sbName) {
                        setActiveSandbox(null);
                    }
                    fetchSandboxes();
                } else {
                    alert("Failed to delete sandbox");
                }
            })
            .catch(err => console.error("Failed to delete sandbox:", err));
    };

    const toggleTaskLogs = (taskName) => {
        if (!activeOverseer || !activeSandbox) return;
        const current = taskLogs[taskName];
        if (current && current.show) {
            setTaskLogs(prev => ({ ...prev, [taskName]: { ...prev[taskName], show: false } }));
            return;
        }

        setTaskLogs(prev => ({ ...prev, [taskName]: { loading: true, show: true, content: 'Loading task execution logs...' } }));

        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes/${activeSandbox.metadata.name}/tasks/${encodeURIComponent(taskName)}/logs`)
            .then(res => res.text())
            .then(data => {
                setTaskLogs(prev => ({ ...prev, [taskName]: { loading: false, show: true, content: data || 'No output log found.' } }));
            })
            .catch(err => {
                setTaskLogs(prev => ({ ...prev, [taskName]: { loading: false, show: true, content: `Error loading log: ${err.message}` } }));
            });
    };

    const getSandboxTypeIcon = (sb) => {
        const t = sb.metadata?.labels?.['sandbox.gemini.google.com/type'] || sb.metadata?.labels?.['sandbox-type'] || 'dev';
        if (t === 'fix') return '🐞';
        if (t === 'review') return '🔄';
        if (t === 'adopt') return '🚀';
        if (t === 'agent') return '⚡';
        if (t.includes('pr')) return '🔨';
        return '📦';
    };

    const getSandboxBadgeLabel = (sb) => {
        const t = sb.metadata?.labels?.['sandbox.gemini.google.com/type'] || sb.metadata?.labels?.['sandbox-type'] || '';
        const name = sb.metadata?.name || '';
        if (t === 'pr' || name.startsWith('factory-pr-') || name.startsWith('pr-')) return 'PR';
        if (t === 'fix' || name.startsWith('factory-fix-') || name.startsWith('fix-')) return 'Fix (Issue->PR)';
        if (t === 'review') return 'Review';
        if (t === 'adopt') return 'Adopt';
        return t || 'Factory';
    };

    const getStatusBadgeColor = (status) => {
        if (!status) return 'var(--status-grey)';
        const s = status.toLowerCase();
        if (s === 'completed' || s === 'ready' || s === 'success' || s === '0') return 'var(--status-green)';
        if (s === 'running' || s === 'provisioning') return 'var(--status-yellow)';
        if (s === 'failed' || s === 'crashed' || s === 'error') return 'var(--text-danger)';
        return 'var(--status-grey)';
    };

    const filterSandbox = (sb) => {
        if (!searchFilter || !searchFilter.trim()) return true;
        const q = searchFilter.trim().toLowerCase();
        const name = sb.metadata?.name || '';
        const issue = sb.metadata?.labels?.['factory.gemini.google.com/issue'] || sb.metadata?.labels?.issue || '';
        const pr = sb.metadata?.labels?.['factory.gemini.google.com/pr'] || sb.metadata?.annotations?.pr || '';
        const user = sb.metadata?.labels?.['factory.gemini.google.com/user'] || '';
        const desc = sb.metadata?.annotations?.['sandbox.gemini.google.com/description'] || '';
        return name.toLowerCase().includes(q) ||
               String(issue).toLowerCase().includes(q) ||
               String(pr).toLowerCase().includes(q) ||
               user.toLowerCase().includes(q) ||
               desc.toLowerCase().includes(q);
    };

    const renderSandboxItem = (sb, isSuspended) => {
        const badgeLabel = getSandboxBadgeLabel(sb);
        const icon = getSandboxTypeIcon(sb);
        const isActive = activeSandbox?.metadata.name === sb.metadata.name && !showOverseerLogs;

        return (
            <div 
                key={sb.metadata.name}
                className={`sidebar-tree-row ${isActive ? 'active' : ''}`}
                onClick={() => handleSandboxClick(sb)}
                style={{ 
                    padding: '8px 15px', 
                    cursor: 'pointer', 
                    display: 'flex', 
                    alignItems: 'center', 
                    justifyContent: 'space-between', 
                    borderRadius: '4px',
                    opacity: isSuspended ? 0.7 : 1
                }}
            >
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px', overflow: 'hidden' }}>
                    <span className="tree-icon">{isSuspended ? '⏸️' : icon}</span>
                    <span className="tree-label" style={{ fontSize: '0.85rem', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', textDecoration: isSuspended ? 'line-through' : 'none' }} title={sb.metadata.name}>
                        {sb.metadata.name}
                    </span>
                </div>
                <span style={{ fontSize: '0.7rem', padding: '2px 6px', borderRadius: '10px', backgroundColor: isSuspended ? '#fff3cd' : 'var(--bg-secondary)', color: isSuspended ? '#856404' : 'var(--text-secondary)', fontWeight: isSuspended ? 'bold' : 'normal', whiteSpace: 'nowrap' }}>
                    {isSuspended ? `Suspended • ${badgeLabel}` : badgeLabel}
                </span>
            </div>
        );
    };

    return (
        <div className="dev-layout" style={{ height: 'calc(100vh - 80px)' }}>
            {/* Sidebar */}
            <div className="dev-sidebar" style={{ width: '320px', borderRight: '1px solid var(--border-color)', overflowY: 'auto', backgroundColor: 'var(--bg-sidebar)' }}>
                <div className="sidebar-header-row" style={{ padding: '15px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <span className="sidebar-header-title" style={{ fontWeight: 'bold', fontSize: '1rem', color: 'var(--text-primary)' }}>Factory & Overseers</span>
                    <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>{overseers.length} Repo(s)</span>
                </div>
                <div className="sidebar-tree-content" style={{ padding: '10px 0' }}>
                    {overseers.map(ov => {
                        const isExpanded = activeOverseer?.metadata.name === ov.metadata.name;
                        return (
                            <React.Fragment key={ov.metadata.name}>
                                <div 
                                    className={`sidebar-tree-row root-row ${isExpanded && showOverseerLogs ? 'active' : ''}`}
                                    onClick={() => handleOverseerClick(ov)}
                                    style={{ padding: '10px 15px', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '8px' }}
                                >
                                    <span className="tree-expander">
                                        {isExpanded ? <ChevronDown /> : <ChevronRight />}
                                    </span>
                                    <span className="tree-icon">📂</span>
                                    <span className="tree-label" style={{ fontWeight: '600' }}>{ov.metadata.name}</span>
                                </div>

                                {isExpanded && (() => {
                                    const filtered = sandboxes.filter(filterSandbox);
                                    const activeSandboxes = filtered.filter(sb => !(sb.spec?.replicas === 0 || sb.spec?.replicas === '0'));
                                    const suspendedSandboxes = filtered.filter(sb => sb.spec?.replicas === 0 || sb.spec?.replicas === '0');

                                    return (
                                        <div style={{ paddingLeft: '15px', display: 'flex', flexDirection: 'column', gap: '2px' }}>
                                            <div 
                                                className={`sidebar-tree-row ${showOverseerLogs ? 'active' : ''}`}
                                                onClick={() => { setShowOverseerLogs(true); setActiveSandbox(null); }}
                                                style={{ padding: '8px 15px', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: '8px', borderRadius: '4px' }}
                                            >
                                                <span className="tree-icon">🤖</span>
                                                <span className="tree-label" style={{ fontSize: '0.9rem' }}>Overseer Daemon Log</span>
                                            </div>

                                            <div style={{ padding: '8px 15px 4px 15px' }}>
                                                <input 
                                                    type="text"
                                                    placeholder="🔍 Filter (PR #, Issue #, wf-)..."
                                                    value={searchFilter}
                                                    onChange={(e) => setSearchFilter(e.target.value)}
                                                    onClick={(e) => e.stopPropagation()}
                                                    style={{
                                                        width: '100%',
                                                        padding: '6px 10px',
                                                        borderRadius: '4px',
                                                        border: '1px solid var(--border-color)',
                                                        backgroundColor: 'var(--bg-card)',
                                                        color: 'var(--text-primary)',
                                                        fontSize: '0.8rem'
                                                    }}
                                                />
                                            </div>

                                            <div style={{ padding: '6px 15px 2px 15px', fontSize: '0.75rem', textTransform: 'uppercase', color: 'var(--text-muted)', fontWeight: 'bold', marginTop: '4px' }}>
                                                Active Sandboxes ({activeSandboxes.length})
                                            </div>

                                            {activeSandboxes.length === 0 && (
                                                <div style={{ padding: '6px 15px', fontSize: '0.8rem', color: 'var(--text-muted)', fontStyle: 'italic' }}>
                                                    {searchFilter ? 'No active sandboxes match filter' : 'No active sandboxes'}
                                                </div>
                                            )}

                                            {activeSandboxes.map(sb => renderSandboxItem(sb, false))}

                                            {suspendedSandboxes.length > 0 && (
                                                <>
                                                    <div style={{ padding: '12px 15px 2px 15px', fontSize: '0.75rem', textTransform: 'uppercase', color: '#856404', fontWeight: 'bold', marginTop: '8px', borderTop: '1px dashed var(--border-color)', display: 'flex', alignItems: 'center', gap: '4px' }}>
                                                        <span>⏸️ Suspended Sandboxes ({suspendedSandboxes.length})</span>
                                                    </div>

                                                    {suspendedSandboxes.map(sb => renderSandboxItem(sb, true))}
                                                </>
                                            )}
                                        </div>
                                    );
                                })()}
                            </React.Fragment>
                        );
                    })}
                </div>
            </div>

            {/* Main Panel */}
            <div className="dev-main" style={{ flex: 1, padding: '25px', overflowY: 'auto', backgroundColor: 'var(--bg-color)' }}>
                {error && (
                    <div className="warning-banner" style={{ backgroundColor: '#fdecea', color: '#721c24', borderColor: '#f5c6cb', marginBottom: '20px', padding: '12px', borderRadius: '4px', border: '1px solid #f5c6cb' }}>
                        <strong>Error fetching overseers:</strong> {error}
                    </div>
                )}

                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '25px', borderBottom: '1px solid var(--border-color)', paddingBottom: '15px' }}>
                    <div>
                        <h2 style={{ margin: 0, fontSize: '1.6rem', color: 'var(--text-primary)', display: 'flex', alignItems: 'center', gap: '10px' }}>
                            {!activeOverseer ? "Select an Overseer Repo from the sidebar" : showOverseerLogs ? `🤖 Overseer: ${activeOverseer.metadata.name}` : `🛠️ Sandbox: ${activeSandbox?.metadata?.name || 'Select a Sandbox'}`}
                        </h2>
                        {activeOverseer && (
                            <p style={{ margin: '5px 0 0 0', fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                                Repo: <a href={activeOverseer.spec?.repoURL} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)' }}>{activeOverseer.spec?.repoURL}</a>
                                {activeOverseer.spec?.pollInterval && ` • Poll: ${activeOverseer.spec?.pollInterval}`}
                                {activeOverseer.spec?.robotAccount && ` • Bot: ${activeOverseer.spec?.robotAccount}`}
                            </p>
                        )}
                    </div>
                    <button className="btn" onClick={onBack} style={{ padding: '8px 16px', fontWeight: '500' }}>Back to Dashboard</button>
                </div>

                {showOverseerLogs ? (
                    <div className="logs-container" style={{ textAlign: 'left' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '12px' }}>
                            <h4 style={{ margin: 0 }}>Overseer Watch Daemon Logs</h4>
                            <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>Live refreshing every 5s...</span>
                        </div>
                        <div className="logs-display" style={{ backgroundColor: '#1e1e1e', color: '#d4d4d4', padding: '18px', borderRadius: '6px', height: '620px', overflowY: 'auto', fontFamily: '"Consolas", "Monaco", "Courier New", monospace', fontSize: '13px', lineHeight: '1.5', boxShadow: 'inset 0 0 10px rgba(0,0,0,0.5)' }}>
                            <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                                {logs || 'Loading daemon logs...'}
                            </pre>
                        </div>
                    </div>
                ) : activeSandbox ? (
                    <div>
                        {/* Sandbox Header Box */}
                        <div style={{ backgroundColor: 'var(--bg-card)', padding: '20px', borderRadius: '8px', border: '1px solid var(--border-color)', marginBottom: '25px', boxShadow: 'var(--shadow-card)' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', flexWrap: 'wrap', gap: '15px' }}>
                                <div>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '10px', marginBottom: '8px' }}>
                                        <span style={{ fontSize: '1.2rem', fontWeight: 'bold', color: 'var(--text-primary)' }}>{activeSandbox.metadata.name}</span>
                                        <span style={{ 
                                            padding: '3px 10px', 
                                            borderRadius: '12px', 
                                            fontSize: '0.75rem', 
                                            fontWeight: 'bold', 
                                            backgroundColor: 'var(--bg-secondary)', 
                                            color: 'var(--text-primary)',
                                            textTransform: 'uppercase'
                                        }}>
                                            {getSandboxBadgeLabel(activeSandbox)}
                                        </span>
                                        {(activeSandbox.spec?.replicas === 0 || activeSandbox.spec?.replicas === '0') && (
                                            <span style={{ 
                                                padding: '3px 10px', 
                                                borderRadius: '12px', 
                                                fontSize: '0.75rem', 
                                                fontWeight: 'bold', 
                                                backgroundColor: '#fff3cd', 
                                                color: '#856404', 
                                                border: '1px solid #ffeeba'
                                            }}>
                                                ⏸️ Suspended (Replicas: 0)
                                            </span>
                                        )}
                                    </div>

                                    <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', display: 'flex', flexWrap: 'wrap', gap: '15px' }}>
                                        <span><strong>Created:</strong> {new Date(activeSandbox.metadata.creationTimestamp).toLocaleString()}</span>
                                        {activeSandbox.metadata?.labels?.['factory.gemini.google.com/user'] && (
                                            <span><strong>User:</strong> {activeSandbox.metadata.labels['factory.gemini.google.com/user']}</span>
                                        )}
                                        {activeSandbox.metadata?.annotations?.pr && (
                                            <span><strong>PR:</strong> #{activeSandbox.metadata.annotations.pr}</span>
                                        )}
                                        {activeSandbox.metadata?.annotations?.htmlURL && (
                                            <span>
                                                <a href={activeSandbox.metadata.annotations.htmlURL} target="_blank" rel="noopener noreferrer" style={{ color: 'var(--text-link)', fontWeight: '600' }}>
                                                    View PR/Issue on GitHub ↗
                                                </a>
                                            </span>
                                        )}
                                    </div>
                                </div>

                                <div style={{ display: 'flex', gap: '10px', alignItems: 'center' }}>
                                    <button 
                                        className="btn" 
                                        style={{ 
                                            backgroundColor: showTerminal ? 'var(--bg-active)' : 'var(--bg-secondary)', 
                                            color: showTerminal ? 'white' : 'var(--text-primary)',
                                            border: '1px solid var(--border-color)',
                                            fontWeight: '600'
                                        }}
                                        onClick={() => { setShowTerminal(!showTerminal); setShowPodLogs(false); }}
                                    >
                                        &gt;_ Live Terminal
                                    </button>

                                    <button 
                                        className="btn" 
                                        style={{ 
                                            backgroundColor: showPodLogs ? 'var(--bg-active)' : 'var(--bg-secondary)', 
                                            color: showPodLogs ? 'white' : 'var(--text-primary)',
                                            border: '1px solid var(--border-color)',
                                            fontWeight: '600'
                                        }}
                                        onClick={() => { setShowPodLogs(!showPodLogs); setShowTerminal(false); }}
                                    >
                                        📑 Pod Logs
                                    </button>

                                    <button 
                                        className="btn" 
                                        style={{ backgroundColor: 'var(--bg-danger-light)', color: 'var(--text-danger)', border: '1px solid var(--border-danger)', fontWeight: '600' }}
                                        onClick={() => handleDeleteSandbox(activeSandbox.metadata.name)}
                                    >
                                        🗑️ Evict / Delete
                                    </button>
                                </div>
                            </div>
                        </div>

                        {/* Terminal View */}
                        {showTerminal && (
                            <div style={{ marginBottom: '25px', borderRadius: '8px', overflow: 'hidden', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                                <div style={{ backgroundColor: '#21262d', color: '#c9d1d9', padding: '10px 15px', fontSize: '0.85rem', fontWeight: 'bold', borderBottom: '1px solid #30363d' }}>
                                    Interactive SSH / Terminal ({activeSandbox.metadata.name})
                                </div>
                                <SandboxTerminal namespace={`overseer-${activeOverseer?.metadata.name}`} sandboxName={activeSandbox.metadata.name} />
                            </div>
                        )}

                        {/* Pod Logs View */}
                        {showPodLogs && (
                            <div className="logs-container" style={{ textAlign: 'left', marginBottom: '25px' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '10px' }}>
                                    <h4 style={{ margin: 0 }}>Full Sandbox Pod Logs</h4>
                                    <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>Live refreshing...</span>
                                </div>
                                <div className="logs-display" style={{ backgroundColor: '#1e1e1e', color: '#d4d4d4', padding: '15px', borderRadius: '6px', height: '450px', overflowY: 'auto', fontFamily: 'monospace', fontSize: '13px' }}>
                                    <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                                        {logs || 'Loading pod logs...'}
                                    </pre>
                                </div>
                            </div>
                        )}

                        {/* Tasks Section */}
                        <div style={{ backgroundColor: 'var(--bg-card)', padding: '20px', borderRadius: '8px', border: '1px solid var(--border-color)', boxShadow: 'var(--shadow-card)' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '15px' }}>
                                <h3 style={{ margin: 0, fontSize: '1.2rem', color: 'var(--text-primary)' }}>
                                    Executed Tasks (`/workspaces/tasks/*`)
                                </h3>
                                <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)' }}>
                                    {tasks.length} task(s) detected inside sandbox
                                </span>
                            </div>

                            {tasks.length === 0 ? (
                                <div style={{ padding: '30px', textAlign: 'center', color: 'var(--text-muted)', backgroundColor: 'var(--bg-review-section)', borderRadius: '6px', border: '1px dashed var(--border-color)' }}>
                                    <p style={{ margin: 0, fontSize: '0.95rem' }}>No tasks found in <code>/workspaces/tasks/</code> for this sandbox yet.</p>
                                </div>
                            ) : (
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                                    {tasks.map((task) => {
                                        const taskName = task.metadata?.name || 'unknown-task';
                                        const taskType = task.spec?.taskType || task.spec?.type || taskName;
                                        const state = task.status?.state || task.status?.taskState || 'Pending';
                                        const exitCode = task.status?.exitCode;
                                        const badgeColor = getStatusBadgeColor(state);
                                        const logInfo = taskLogs[taskName];

                                        return (
                                            <div key={taskName} style={{ border: '1px solid var(--border-color)', borderRadius: '6px', backgroundColor: 'var(--bg-review-section)', overflow: 'hidden' }}>
                                                <div style={{ padding: '14px 18px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: '10px' }}>
                                                    <div>
                                                        <span style={{ fontWeight: 'bold', fontSize: '1rem', color: 'var(--text-primary)' }}>{taskType}</span>
                                                        <span style={{ fontSize: '0.8rem', color: 'var(--text-muted)', marginLeft: '12px', fontFamily: 'monospace' }}>
                                                            {taskName}
                                                        </span>
                                                    </div>

                                                    <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
                                                        {exitCode !== undefined && exitCode !== null && (
                                                            <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', fontFamily: 'monospace' }}>
                                                                Exit Code: <strong>{exitCode}</strong>
                                                            </span>
                                                        )}

                                                        <span style={{ 
                                                            backgroundColor: badgeColor, 
                                                            color: state.toLowerCase() === 'running' || state.toLowerCase() === 'provisioning' ? 'black' : 'white', 
                                                            padding: '4px 10px', 
                                                            borderRadius: '12px', 
                                                            fontSize: '0.75rem', 
                                                            fontWeight: 'bold',
                                                            textTransform: 'uppercase'
                                                        }}>
                                                            {state}
                                                        </span>

                                                        <button 
                                                            className="btn btn-sm" 
                                                            style={{ 
                                                                backgroundColor: logInfo?.show ? 'var(--bg-active)' : 'var(--bg-card)', 
                                                                color: logInfo?.show ? 'white' : 'var(--text-primary)',
                                                                border: '1px solid var(--border-color)',
                                                                fontSize: '0.8rem',
                                                                padding: '5px 12px'
                                                            }}
                                                            onClick={() => toggleTaskLogs(taskName)}
                                                        >
                                                            {logInfo?.show ? 'Hide Logs' : 'View Execution Log'}
                                                        </button>
                                                    </div>
                                                </div>

                                                {logInfo?.show && (
                                                    <div style={{ borderTop: '1px solid var(--border-color)', padding: '15px', backgroundColor: '#161b22', color: '#c9d1d9', textAlign: 'left' }}>
                                                        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '8px', fontSize: '0.8rem', color: '#8b949e', textAlign: 'left' }}>
                                                            <span>Output (`/workspaces/tasks/{taskName}/execution.log`)</span>
                                                        </div>
                                                        <pre style={{ margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'monospace', fontSize: '12px', maxHeight: '400px', overflowY: 'auto', lineHeight: '1.5', textAlign: 'left' }}>
                                                            {logInfo.loading ? 'Loading log...' : logInfo.content}
                                                        </pre>
                                                    </div>
                                                )}
                                            </div>
                                        );
                                    })}
                                </div>
                            )}
                        </div>
                    </div>
                ) : (
                    <div style={{ textAlign: 'center', padding: '60px 20px', color: 'var(--text-muted)', backgroundColor: 'var(--bg-card)', borderRadius: '8px', border: '1px dashed var(--border-color)' }}>
                        <h3 style={{ margin: '0 0 10px 0', fontSize: '1.3rem', color: 'var(--text-secondary)' }}>Welcome to the Overseer Factory Control Center</h3>
                        <p style={{ margin: 0, fontSize: '0.95rem', maxWidth: '600px', display: 'inline-block', lineHeight: '1.6' }}>
                            Select an <strong>Overseer Repo</strong> from the sidebar on the left to inspect its active <strong>Factory Sandboxes</strong> (`pr-iterate-*`, `fix-*`, `adopt-*`, `agent-*`) and peek directly inside `/workspaces/tasks/*` for live task execution status and log streaming.
                        </p>
                    </div>
                )}
            </div>
        </div>
    );
};

export default Overseer;
