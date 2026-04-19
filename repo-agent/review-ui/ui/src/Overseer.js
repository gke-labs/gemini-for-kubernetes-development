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

import React, { useState, useEffect, useCallback, useRef } from 'react';
import './App.css';
import TaskCard from './TaskCard';
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

const Overseer = ({ onBack, getSandboxStatusClass, namespace: userNamespace }) => {

    const [overseers, setOverseers] = useState([]);
    const [error, setError] = useState(null);
    const [activeOverseer, setActiveOverseer] = useState(null);
    const [chores, setChores] = useState([]);
    const [sandboxes, setSandboxes] = useState([]);
    const [activeChore, setActiveChore] = useState(null);
    const [tasks, setTasks] = useState([]);
    
    const [logs, setLogs] = useState('');
    const [loading, setLoading] = useState(false);
    const [showOverseerLogs, setShowOverseerLogs] = useState(false);
    const [showTerminal, setShowTerminal] = useState(false);

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
                        const updated = (data || []).find(o => o.metadata.name === prev.metadata.name);
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
    }, [fetchOverseers]);

    const fetchSandboxes = useCallback(() => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/sandboxes`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch sandboxes");
                return res.json();
            })
            .then(data => setSandboxes(data || []))
            .catch(err => {
                console.error("Failed to fetch sandboxes:", err);
                setSandboxes([]);
            });
    }, [activeOverseer]);

    const fetchChores = useCallback(() => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch chores");
                return res.json();
            })
            .then(data => setChores(data || []))
            .catch(err => {
                console.error("Failed to fetch chores:", err);
                setChores([]);
            });
    }, [activeOverseer]);

    useEffect(() => {
        fetchChores();
        fetchSandboxes();
        const interval = setInterval(() => { fetchChores(); fetchSandboxes(); }, 10000);
        return () => clearInterval(interval);
    }, [fetchChores]);

    const fetchOverseerLogs = useCallback(() => {
        if (!activeOverseer || !showOverseerLogs) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/logs`)
            .then(res => res.text())
            .then(data => setLogs(data))
            .catch(err => console.error("Failed to fetch overseer logs:", err));
    }, [activeOverseer, showOverseerLogs]);

    const fetchChoreTasks = useCallback(() => {
        if (!activeOverseer || !activeChore) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores/${activeChore.metadata.name}/tasks`)
            .then(res => {
                if (!res.ok) throw new Error("Failed to fetch chore tasks");
                return res.json();
            })
            .then(data => {
                setTasks(data || []);
                
            })
            .catch(err => {
                console.error("Failed to fetch chore tasks:", err);
                setTasks([]);
            });
    }, [activeOverseer, activeChore]);

    

    useEffect(() => {
        if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        
        if (showOverseerLogs) {
            fetchOverseerLogs();
            logIntervalRef.current = setInterval(fetchOverseerLogs, 5000);
        } else {
            setLogs('');
        }

        return () => {
            if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        };
    }, [showOverseerLogs, fetchOverseerLogs]);

    useEffect(() => {
        if (activeChore) {
            fetchChoreTasks();
            const interval = setInterval(fetchChoreTasks, 10000);
            return () => clearInterval(interval);
        }
    }, [activeChore, fetchChoreTasks]);

    const handleOverseerClick = (ov) => {
        if (activeOverseer?.metadata.name === ov.metadata.name) {
            setActiveOverseer(null);
            setActiveChore(null);
            
            setShowOverseerLogs(false);
        } else {
            setActiveOverseer(ov);
            setActiveChore(null);
            
            setShowOverseerLogs(true);
        }
    };


    const handlePauseChore = (choreName) => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores/${choreName}/pause`, { method: 'POST' })
            .then(res => {
                if (res.ok) {
                    fetchChores();
                    fetchOverseers();
                    setActiveChore(null);
                } else {
                    alert("Failed to pause chore");
                }
            })
            .catch(err => console.error("Failed to pause chore:", err));
    };

    const handleResumeChore = (choreName) => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores/${choreName}/resume`, { method: 'POST' })
            .then(res => {
                if (res.ok) {
                    fetchChores();
                    fetchOverseers();
                    setActiveChore(null);
                } else {
                    alert("Failed to resume chore");
                }
            })
            .catch(err => console.error("Failed to resume chore:", err));
    };

    const handleChoreClick = (chore) => {
        setActiveChore(chore);
        
        setShowOverseerLogs(false);
    };

    return (
        <div className="dev-layout" style={{ height: 'calc(100vh - 80px)' }}>
            <div className="dev-sidebar" style={{ width: '300px', borderRight: '1px solid var(--border-color)', overflowY: 'auto' }}>
                <div className="sidebar-header-row">
                    <span className="sidebar-header-title">Overseer Repos</span>
                </div>
                <div className="sidebar-tree-content">
                    {overseers.map(ov => {
                        const isExpanded = activeOverseer?.metadata.name === ov.metadata.name;
                        return (
                            <React.Fragment key={ov.metadata.name}>
                                <div 
                                    className={`sidebar-tree-row root-row ${isExpanded && showOverseerLogs && !activeChore ? 'active' : ''}`}
                                    onClick={() => handleOverseerClick(ov)}
                                >
                                    <span className="tree-expander">
                                        {isExpanded ? <ChevronDown /> : <ChevronRight />}
                                    </span>
                                    <span className="tree-icon">📂</span>
                                    <span className="tree-label">{ov.metadata.name}</span>
                                </div>
                                {isExpanded && (
                                    <>
                                        <div 
                                            className={`sidebar-tree-row ${showOverseerLogs ? 'active' : ''}`}
                                            onClick={() => { setShowOverseerLogs(true); setActiveChore(null); }}
                                            style={{ paddingLeft: '35px' }}
                                        >
                                            <span className="tree-icon">🤖</span>
                                            <span className="tree-label">Overseer Agent</span>
                                        </div>
                                        {chores.length === 0 && <div style={{ paddingLeft: '35px', fontSize: '0.8rem', color: 'var(--text-muted)', marginTop: '5px' }}>No chores found</div>}
                                        {chores.map(chore => (
                                            <div 
                                                key={chore.metadata.name}
                                                className={`sidebar-tree-row ${activeChore?.metadata.name === chore.metadata.name ? 'active' : ''}`}
                                                onClick={() => handleChoreClick(chore)}
                                                style={{ paddingLeft: '35px' }}
                                            >
                                                <span className="tree-icon">⚙️</span>
                                                <span className="tree-label">{chore.metadata.labels?.['chore.gemini.google.com/name'] || chore.metadata.name}</span>
                                            </div>
                                        ))}
                                        
                                        {(() => {
                                            const excluded = activeOverseer.spec?.chores?.exclude || [];
                                            return excluded.map(choreName => (
                                                <div 
                                                    key={choreName}
                                                    className={`sidebar-tree-row ${activeChore?.metadata?.name === choreName ? 'active' : ''}`}
                                                    onClick={() => handleChoreClick({ metadata: { name: choreName, isExcluded: true } })}
                                                    style={{ paddingLeft: '35px', opacity: 0.6 }}
                                                >
                                                    <span className="tree-icon">⏸️</span>
                                                    <span className="tree-label">{choreName} (Paused)</span>
                                                </div>
                                            ));
                                        })()}

                                        {sandboxes.map(sb => {
                                            const type = sb.metadata.labels?.['sandbox-type'] || 'dev';
                                            const icon = type === 'review' ? '🔄' : type === 'issue' ? '🐞' : '🛠️';
                                            const name = sb.metadata.labels?.['chore.gemini.google.com/name'] || sb.metadata.name;
                                            return (
                                                <div 
                                                    key={sb.metadata.name}
                                                    className={`sidebar-tree-row ${activeChore?.metadata.name === sb.metadata.name ? 'active' : ''}`}
                                                    onClick={() => handleChoreClick(sb)}
                                                    style={{ paddingLeft: '35px' }}
                                                >
                                                    <span className="tree-icon">{icon}</span>
                                                    <span className="tree-label">{name}</span>
                                                </div>
                                            );
                                        })}
                                    </>
                                )}
                            </React.Fragment>
                        );
                    })}
                </div>
            </div>

            <div className="dev-main">
                {error && (
                    <div className="warning-banner" style={{ backgroundColor: '#fdecea', color: '#721c24', borderColor: '#f5c6cb', marginBottom: '20px', padding: '10px', borderRadius: '4px', border: '1px solid #f5c6cb' }}>
                        <strong>Error fetching overseers:</strong> {error}
                    </div>
                )}
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
                    <h2>
                        {!activeOverseer ? "Select an item" : showOverseerLogs ? `Overseer: ${activeOverseer.metadata.name}` : `Chore: ${activeChore?.metadata?.labels?.['chore.gemini.google.com/name'] || activeChore?.metadata.name}`}
                    </h2>
                    <button className="btn" onClick={onBack}>Back to Dashboard</button>
                </div>

                {showOverseerLogs ? (
                    <div className="logs-container" style={{ textAlign: 'left' }}>
                        <h4>Logs</h4>
                        <div className="logs-display" style={{ backgroundColor: '#1e1e1e', color: '#d4d4d4', padding: '15px', borderRadius: '5px', height: '600px', overflowY: 'auto', fontFamily: '"Consolas", "Monaco", "Courier New", monospace', fontSize: '13px', lineHeight: '1.5' }}>
                            <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                                {logs || 'Loading logs...'}
                            </pre>
                        </div>
                    </div>
                ) : activeChore ? (
                    <div className="pr-card" style={{ marginBottom: '20px' }}>
                        <div className="pr-card-header">
                            <h3>{activeChore.metadata?.labels?.['chore.gemini.google.com/name'] || activeChore.metadata.name}</h3>
                            <div className="pr-card-actions-header">
                                
                                            {activeChore.metadata.isExcluded ? (
                                                <button className="btn btn-sm" style={{backgroundColor: 'var(--status-green)', color: 'white'}} onClick={(e) => { e.stopPropagation(); handleResumeChore(activeChore.metadata.name); }}>
                                                    ▶ Resume Chore
                                                </button>
                                            ) : (
                                                <button className="btn btn-sm" style={{backgroundColor: 'var(--status-yellow)', color: 'black'}} onClick={(e) => { e.stopPropagation(); handlePauseChore(activeChore.metadata.name); }}>
                                                    ⏸ Pause Chore
                                                </button>
                                            )}

                                {(() => {
                                    if (!getSandboxStatusClass || activeChore.metadata.isExcluded) return null;
                                    const overseerNamespace = `overseer-${activeOverseer?.metadata.name}`;
                                    const mappedChore = {
                                        sandbox: activeChore.metadata.name,
                                        sandboxStatus: activeChore.status?.conditions?.[0]?.message || '',
                                        agentState: activeChore.metadata?.annotations?.agentState || ''
                                    };
                                    return (
                                    <React.Fragment>
                                        {getSandboxStatusClass(mappedChore) === 'green' ? (
                                            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
                                                <button 
                                                    className="btn btn-sm" 
                                                    style={{
                                                        backgroundColor: showTerminal ? 'var(--bg-active)' : 'transparent', 
                                                        color: 'var(--text-primary)', 
                                                        padding: '4px 8px', 
                                                        border: '1px solid var(--border-color)',
                                                        fontFamily: 'monospace',
                                                        fontWeight: 'bold'
                                                    }}
                                                    onClick={(e) => { e.stopPropagation(); setShowTerminal(!showTerminal); }}
                                                    title={showTerminal ? "Hide Terminal" : "Show Terminal"}
                                                >
                                                    &gt;_
                                                </button>
                                                {activeChore.status?.state === 'provisioning' ? (
                                                    <span className="pr-sandbox" style={{backgroundColor: 'var(--text-link)', color: 'white', cursor: 'default'}}>
                                                    Sandbox Provisioning...
                                                    </span>
                                                ) : (
                                                    <a href={`/sandbox/${overseerNamespace}/${activeChore.metadata.name}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(mappedChore)}`}>
                                                    Sandbox
                                                    </a>
                                                )}
                                            </div>
                                        ) : getSandboxStatusClass(mappedChore) === 'yellow' ? (
                                            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
                                                <span className={`pr-sandbox ${getSandboxStatusClass(mappedChore)}`}>Sandbox</span>
                                            </div>
                                        ) : getSandboxStatusClass(mappedChore) === 'red' ? (
                                            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
                                                <span className={`pr-sandbox ${getSandboxStatusClass(mappedChore)}`} title={activeChore.status?.message || 'Error'}>
                                                    {activeChore.status?.message?.startsWith('Evicted') ? 'Evicted' : (activeChore.status?.message || 'Error')}
                                                </span>
                                            </div>
                                        ) : (
                                            <span className={`pr-sandbox ${getSandboxStatusClass(mappedChore)}`}>Sandbox: Not created</span>
                                        )}
                                    </React.Fragment>
                                    );
                                })()}
                            </div>
                        </div>

                        {showTerminal && getSandboxStatusClass && getSandboxStatusClass({
                            sandbox: activeChore.metadata.name,
                            sandboxStatus: activeChore.status?.conditions?.[0]?.message || '',
                            agentState: activeChore.metadata?.annotations?.agentState || ''
                        }) === 'green' && !activeChore.metadata.isExcluded && (
                            <div style={{ borderBottom: '1px solid var(--border-color)' }}>
                                <SandboxTerminal namespace={`overseer-${activeOverseer?.metadata.name}`} sandboxName={activeChore.metadata.name} />
                            </div>
                        )}

                        <div style={{padding: '10px'}}>
                            {activeChore.metadata.isExcluded ? (
                                <p style={{color: 'var(--text-muted)'}}>This chore is currently paused.</p>
                            ) : tasks.length > 0 ? (
                                tasks.slice().reverse().map((task, index) => (
                                    <TaskCard 
                                        key={task.metadata.name} 
                                        task={{
                                            name: task.metadata.name,
                                            type: task.spec.type,
                                            creationTimestamp: task.metadata.creationTimestamp,
                                            agentStateMessage: task.status?.message || '',
                                            agentState: task.status?.state || '',
                                            result: task.status?.result || ''
                                        }} 
                                        repoName={activeOverseer?.metadata.name} 
                                        parentId={activeChore.metadata.name}
                                        parentType="chores"
                                        defaultCollapsed={index !== tasks.length - 1}
                                    />
                                ))
                            ) : (
                                <p style={{color: 'var(--text-muted)'}}>No tasks found for this chore.</p>
                            )}
                        </div>
                    </div>
                ) : null}
            </div>
        </div>
    );
};

export default Overseer;
