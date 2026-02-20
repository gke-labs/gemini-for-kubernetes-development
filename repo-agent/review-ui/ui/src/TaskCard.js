import React, { useState, useEffect } from 'react';

function TaskCard({
    task,
    repoName,
    parentId, // issueId or sandboxName
    parentType, // 'issues' or 'dev' or 'prs'
    handleScaleUp,
    defaultCollapsed = false,
    showToast,
}) {
    const [localDraft, setLocalDraft] = useState(task.userDraft || task.agentDraft || '');
    const [isCollapsed, setIsCollapsed] = useState(defaultCollapsed);
    const [statusText, setStatusText] = useState('');
    const [showLogs, setShowLogs] = useState(false);
    const [logs, setLogs] = useState('');

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
            };
            fetchLogs();
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
        if (t === 'ready' || t === 'completed') return '#22c55e';
        if (t === 'running') return '#f59e0b';
        if (t === 'failed') return '#ef4444';
        if (t === 'submitted') return '#3b82f6';
        return '#f59e0b';
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
                    if (showToast) showToast("Comment submitted!", 'success');
                } else {
                    res.text().then(t => { if (showToast) showToast("Failed to submit: " + t, 'error'); });
                }
            })
            .catch(err => console.error("Failed to submit comment", err));
        } else {
            if (showToast) showToast("Submission for this task type is not yet implemented.", 'info');
        }
    };
    const isSubmittable = task.agentDraftType === 'submittable';
    return (
        <div className="card-animate-in" style={{border: '1px solid var(--border-color)', borderRadius: '12px', margin: '10px 0', backgroundColor: 'var(--bg-review-section)', overflow: 'hidden'}}>
            {/* Task header with icon */}
            <div
                style={{padding: '12px 16px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer', backgroundColor: 'var(--bg-hover)'}}
                onClick={() => setIsCollapsed(!isCollapsed)}
            >
                <div style={{display: 'flex', alignItems: 'center', gap: '10px'}}>
                    <span className="material-symbols-outlined" style={{fontSize: '20px', color: task.type === 'triage-issue' ? '#f59e0b' : task.type === 'fix-issue' ? '#22c55e' : 'var(--color-primary)'}}>
                        {task.type === 'triage-issue' ? 'troubleshoot' : task.type === 'fix-issue' ? 'auto_fix_high' : task.type === 'iterate' ? 'sync' : 'task'}
                    </span>
                    <strong style={{fontSize: '13px'}}>{task.type.toUpperCase()}</strong>
                    <span style={{ fontSize: '11px', color: 'var(--text-muted)' }}>
                        {new Date(task.creationTimestamp).toLocaleString()}
                    </span>
                    <span className="material-symbols-outlined" style={{fontSize: '16px', color: 'var(--text-muted)'}}>{isCollapsed ? 'chevron_right' : 'expand_more'}</span>
                </div>
                <span
                    className={statusText.toLowerCase() === 'running' ? 'status-badge-running' : ''}
                    style={{ backgroundColor: getStatusColor(statusText) + '1a', color: getStatusColor(statusText), padding: '2px 6px', borderRadius: '4px', fontSize: '10px', fontWeight: '700', textTransform: 'uppercase' }}
                    title={task.agentStateMessage}
                >
                    {statusText}
                </span>
            </div>

            {!isCollapsed && (
                <div style={{padding: '16px'}}>
                    <div style={{ display: 'flex', justifyContent: 'flex-end', paddingBottom: '12px', gap: '10px' }}>
                        <button className="btn btn-secondary" style={{padding: '6px 12px', fontSize: '12px'}} onClick={() => setShowLogs(!showLogs)}>
                            <span className="material-symbols-outlined" style={{fontSize: '16px'}}>{showLogs ? 'visibility_off' : 'terminal'}</span>
                            {showLogs ? 'Hide Logs' : 'View Logs'}
                        </button>
                    </div>
                    {showLogs && (
                        <div className="logs-display">
                            <pre>{logs || 'Loading logs...'}</pre>
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
                            borderRadius: '8px',
                            marginBottom: '10px',
                            border: '1px solid var(--border-color)',
                            overflowX: 'auto'
                        }}>
                             <pre style={{margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'var(--font-mono)', fontSize: '13px'}}>{localDraft}</pre>
                        </div>
                    )}
                </div>
            )}
        </div>
    );
}

export default TaskCard;
