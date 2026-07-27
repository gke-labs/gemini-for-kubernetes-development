import React, { useState, useEffect } from 'react';

function TaskCard({
    task,
    repoName,
    parentId, // issueId or sandboxName
    parentType, // 'issues' or 'dev' or 'prs'
    handleScaleUp,
    defaultCollapsed = false,
}) {
    const [localDraft, setLocalDraft] = useState(task.userDraft || task.agentDraft || '');
    const [isCollapsed, setIsCollapsed] = useState(defaultCollapsed);
    const [statusText, setStatusText] = useState('');
    const [showLogs, setShowLogs] = useState(false);
    const [logs, setLogs] = useState('');
    const [telemetry, setTelemetry] = useState(null);

    useEffect(() => {
        const content = task.userDraft || task.agentDraft || '';
        if (content !== localDraft) {
            setLocalDraft(content);
        }
    }, [task.userDraft, task.agentDraft]);

    useEffect(() => {
        let isMounted = true;
        let timeoutId;

        if (showLogs && repoName) {
            const fetchLogs = () => {
                const url = `/api/repo/${encodeURIComponent(repoName)}/${parentType}/${encodeURIComponent(parentId)}/tasks/${encodeURIComponent(task.name)}/logs`;
                const telemetryUrl = `/api/repo/${encodeURIComponent(repoName)}/${parentType}/${encodeURIComponent(parentId)}/tasks/${encodeURIComponent(task.name)}/telemetry`;
                
                fetch(url)
                .then(res => {
                    if (res.ok) return res.text();
                    throw new Error("Failed to load logs");
                })
                .then(text => {
                    if (isMounted) {
                        setLogs(text);
                        timeoutId = setTimeout(fetchLogs, 5000);
                    }
                })
                .catch(err => {
                    if (isMounted) {
                        setLogs(`Error loading logs: ${err.message}`);
                        timeoutId = setTimeout(fetchLogs, 5000);
                    }
                });

                fetch(telemetryUrl)
                .then(res => res.json())
                .then(data => {
                    if (isMounted && data && data.total_tool_calls > 0) {
                        setTelemetry(data);
                    }
                })
                .catch(() => {});
            };
            fetchLogs();
        } else {
            setLogs('');
            setTelemetry(null);
        }
        
        return () => {
            isMounted = false;
            clearTimeout(timeoutId);
        };
    }, [showLogs, repoName, parentType, parentId, task.name]);

    useEffect(() => {
        if (task.taskState === 'Completed') {
             if (task.result === 'submitted') {
                 setStatusText('Submitted');
             } else {
                 setStatusText('Ready');
             }
        } else if (task.taskState === 'Running') {
             setStatusText('Running');
        } else if (task.taskState === 'Failed') {
             setStatusText('Failed');
        } else {
             setStatusText(task.taskState || 'Pending');
        }
    }, [task.taskState, task.result]);

    const getStatusColor = (text) => {
        const t = text.toLowerCase();
        if (t === 'ready' || t === 'completed') return 'green';
        if (t === 'running') return 'orange';
        if (t === 'failed') return '#9e2a2aff';
        return '#cd9945ff';
    };

    const handleSaveDraft = () => {
        // This endpoint might need adjustment if generic task draft saving is different
        // Currently handlers_issue.go has saveIssueDraft but not per task draft saving except in server.go:
        // api.POST("/repo/:repo/tasks/:taskID/draft", s.saveTaskDraft)
        // Let's use the generic one if available or assume parentType structure
        
        // Actually looking at server.go: api.POST("/repo/:repo/tasks/:taskID/draft", s.saveTaskDraft)
        // This seems generic enough!
        fetch(`/api/repo/${repoName}/tasks/${task.name}/draft`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ draft: localDraft })
        }).catch(err => console.error("Failed to save task draft", err));
    };

    const handleSubmit = () => {
        if (parentType === 'issues') {
            fetch(`/api/repo/${repoName}/issues/${parentId}/submitcomment`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ comment: localDraft })
            })
            .then(res => {
                if (res.ok) {
                    alert("Comment submitted!");
                } else {
                    res.text().then(t => alert("Failed to submit: " + t));
                }
            })
            .catch(err => console.error("Failed to submit comment", err));
        } else {
            // For Dev/PR tasks, we might not have a direct "submit comment" equivalent that posts to GitHub yet
            // or we might want to just save it as a note or trigger some other action.
            // For now, let's just alert
            alert("Submission for this task type is not yet implemented.");
        }
    };
    const isSubmittable = task.agentDraftType === 'submittable';
    return (
        <div style={{border: '1px solid var(--border-color)', borderRadius: '5px', margin: '10px 0', backgroundColor: 'var(--bg-review-section)'}}>
            <div 
                style={{padding: '10px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer', backgroundColor: 'var(--bg-hover)'}}
                onClick={() => setIsCollapsed(!isCollapsed)}
            >
                <div>
                    <strong>{(task.type || 'Unknown').toUpperCase()}</strong>
                    <span style={{ fontSize: 'small', color: 'var(--text-secondary)', marginLeft: '10px' }}>
                        {new Date(task.creationTimestamp).toLocaleString()}
                    </span>
                </div>
                <span 
                    style={{ backgroundColor: getStatusColor(statusText), color: 'white', padding: '2px 8px', borderRadius: '4px', fontSize: 'small' }}
                    title={task.agentStateMessage}
                >
                    {statusText}
                </span>
            </div>
            
            {!isCollapsed && (
                <div style={{padding: '15px'}}>
                    <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '10px 0', gap: '10px' }}>
                        <button className="btn" onClick={() => setShowLogs(!showLogs)}>
                            {showLogs ? 'Hide Logs' : 'View Logs'}
                        </button>
                    </div>
                    {showLogs && (
                        <div>
                            {telemetry && telemetry.total_tool_calls > 0 && (
                                <div style={{ marginBottom: '10px', padding: '10px', backgroundColor: '#1e1e1e', border: '1px solid #444', borderRadius: '5px' }}>
                                    <div style={{ fontWeight: 'bold', color: '#58a6ff', marginBottom: '6px', fontSize: '13px' }}>
                                        ⚡ Tool Execution Telemetry ({telemetry.total_tool_calls} calls, {telemetry.total_tool_duration_sec}s total)
                                    </div>
                                    <div style={{ overflowX: 'auto' }}>
                                        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '12px', textAlign: 'left' }}>
                                            <thead>
                                                <tr style={{ borderBottom: '1px solid #333', color: '#aaa' }}>
                                                    <th style={{ padding: '4px 6px' }}>Tool</th>
                                                    <th style={{ padding: '4px 6px' }}>Calls</th>
                                                    <th style={{ padding: '4px 6px' }}>Total (s)</th>
                                                    <th style={{ padding: '4px 6px' }}>Max (s)</th>
                                                    <th style={{ padding: '4px 6px' }}>Slowest Command</th>
                                                </tr>
                                            </thead>
                                            <tbody>
                                                {Object.entries(telemetry.tools || {}).map(([tname, tstat]) => (
                                                    <tr key={tname} style={{ borderBottom: '1px solid #333' }}>
                                                        <td style={{ padding: '4px 6px', fontFamily: 'monospace', color: '#7ee787' }}>{tname}</td>
                                                        <td style={{ padding: '4px 6px' }}>{tstat.count}</td>
                                                        <td style={{ padding: '4px 6px' }}>{tstat.total_sec}</td>
                                                        <td style={{ padding: '4px 6px', color: tstat.max_sec > 60 ? '#ff7b72' : 'inherit', fontWeight: tstat.max_sec > 60 ? 'bold' : 'normal' }}>{tstat.max_sec}</td>
                                                        <td style={{ padding: '4px 6px', fontFamily: 'monospace', fontSize: '11px', color: '#aaa', maxWidth: '250px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={tstat.slowest_cmd}>{tstat.slowest_cmd || '-'}</td>
                                                    </tr>
                                                ))}
                                            </tbody>
                                        </table>
                                    </div>
                                </div>
                            )}
                            <div className="logs-display" style={{backgroundColor: '#333', color: '#fff', padding: '10px', borderRadius: '5px', marginBottom: '10px', maxHeight: '300px', overflowY: 'auto'}}>
                                <pre style={{margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'monospace'}}>{logs || 'Loading logs...'}</pre>
                            </div>
                        </div>
                    )}
                    {isSubmittable ? (
                        <>
                            <textarea
                                className="review-textarea"
                                value={localDraft}
                                onChange={(e) => setLocalDraft(e.target.value)}
                                onBlur={handleSaveDraft}
                                placeholder="Agent output or your comment..."
                                rows={10}
                                style={{width: '100%', marginBottom: '10px'}}
                            />
                            <div className="pr-card-actions">
                                <button className="btn btn-submit" onClick={handleSubmit}>Submit Comment</button>
                            </div>
                        </>
                    ) : (
                         localDraft && <div style={{
                            backgroundColor: 'var(--bg-secondary)', 
                            padding: '10px', 
                            borderRadius: '5px', 
                            marginBottom: '10px',
                            border: '1px solid var(--border-color)',
                            overflowX: 'auto'
                        }}>
                             <pre style={{margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'monospace'}}>{localDraft}</pre>
                        </div>
                    )}
                </div>
            )}
        </div>
    );
}

export default TaskCard;
