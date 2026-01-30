import React, { useState, useEffect } from 'react';
import yaml from 'js-yaml';

function TaskIssueCard({
    task,
    repoName,
    issueId,
    handleScaleUp, // Optional: if we want to scale up on interaction
}) {
    const [localDraft, setLocalDraft] = useState(task.userDraft || task.agentDraft || '');
    const [isCollapsed, setIsCollapsed] = useState(false);
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
                fetch(`/api/repo/${encodeURIComponent(repoName)}/issues/${encodeURIComponent(issueId)}/tasks/${encodeURIComponent(task.name)}/logs`)
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
    }, [showLogs, repoName, issueId, task.name]);

    useEffect(() => {
        if (task.taskState === 'Completed') {
             if (task.result === 'submitted') { // Assuming we track submission state in result or similar
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
        fetch(`/api/repo/${repoName}/tasks/${task.name}/draft`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ draft: localDraft })
        }).catch(err => console.error("Failed to save task draft", err));
    };

    const handleSubmit = () => {
        // We submit the draft as a comment to the issue
        // We use the generic submitComment endpoint
        fetch(`/api/repo/${repoName}/issues/${issueId}/submitcomment`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ comment: localDraft })
        })
        .then(res => {
            if (res.ok) {
                alert("Comment submitted!");
                // Ideally we update local state or refresh
            } else {
                res.text().then(t => alert("Failed to submit: " + t));
            }
        })
        .catch(err => console.error("Failed to submit comment", err));
    };

    return (
        <div style={{border: '1px solid #ddd', borderRadius: '5px', margin: '10px 0', backgroundColor: '#f9f9f9'}}>
            <div 
                style={{padding: '10px', borderBottom: '1px solid #ddd', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer', backgroundColor: '#eee'}}
                onClick={() => setIsCollapsed(!isCollapsed)}
            >
                <div>
                    <strong>{task.type.toUpperCase()}</strong> {/* Display generic name like TRIAGE */}
                    <span style={{ fontSize: 'small', color: '#555', marginLeft: '10px' }}>
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
                        <div className="logs-display" style={{backgroundColor: '#333', color: '#fff', padding: '10px', borderRadius: '5px', marginBottom: '10px', maxHeight: '300px', overflowY: 'auto'}}>
                            <pre style={{margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'monospace'}}>{logs || 'Loading logs...'}</pre>
                        </div>
                    )}
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
                </div>
            )}
        </div>
    );
}

function IssueCard({
  issue,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
  handleIssueDelete,
  repoName,
  isMainView,
  drafts,
  activeSubTab,
  handleIssueDraftChange,
  handleIssueSaveDraft,
  handleIssueSubmit,
}) {
  const [isCollapsed, setIsCollapsed] = useState(!isMainView);
  const [tasks, setTasks] = useState([]);

  const fetchTasks = () => {
    if (!repoName || !issue.id) return;
    fetch(`/api/repo/${repoName}/issues/${issue.id}/tasks`)
        .then(res => res.json())
        .then(data => {
            if (Array.isArray(data)) {
                setTasks(data);
            }
        })
        .catch(err => console.error("Failed to fetch tasks:", err));
  };

  const handleCreateTask = (taskType, prompt = '') => {
      if (!repoName || !issue.id) return;
      fetch(`/api/repo/${repoName}/issues/${issue.id}/tasks`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ taskType, prompt })
      })
      .then(res => {
          if (res.ok) {
              alert(`Task ${taskType} started!`);
              fetchTasks();
          } else {
              res.text().then(t => alert("Failed to create task: " + t));
          }
      })
      .catch(err => console.error("Failed to create task", err));
  };

  useEffect(() => {
    if (isMainView || !isCollapsed) {
        fetchTasks();
        const interval = setInterval(fetchTasks, 10000);
        return () => clearInterval(interval);
    }
  }, [isCollapsed, isMainView, repoName, issue.id]);

  return (
    <div key={issue.id} className="pr-card">
      <div className="pr-card-header" onClick={() => !isMainView && setIsCollapsed(!isCollapsed)} style={isMainView ? {cursor: 'default'} : {}}>
        <h3>
          <a href={issue.htmlURL} target="_blank" rel="noopener noreferrer">{issue.title} (Issue #{issue.id})</a>
          {!isMainView && (
            <span style={{ marginLeft: '10px', fontSize: 'small', color: '#555' }}>
                {isCollapsed ? 'click to expand' : 'click to collapse'}
            </span>
          )}
        </h3>
        <div className="pr-card-actions-header">
          {issue.labels && issue.labels.length > 0 && (
            <div style={{ display: 'flex', gap: '5px', marginRight: '10px' }}>
              {issue.labels.map((label, index) => (
                <span
                  key={index}
                  style={{
                    backgroundColor: '#eee',
                    color: '#333',
                    padding: '2px 6px',
                    borderRadius: '4px',
                    fontSize: 'small',
                    border: '1px solid #ddd'
                  }}
                >
                  {label}
                </span>
              ))}
            </div>
          )}
          {getSandboxStatusClass(issue) === 'green' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <a href={`/sandbox/${namespace}/${issue.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>
                  Sandbox Active
              </a>
               <button className="btn btn-sm pr-sandbox yellow" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleDown(issue.id); }} title="Scale Down">
                &#9646;&#9646;
              </button>
            </div>
          ) : getSandboxStatusClass(issue) === 'yellow' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox Paused</span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(issue.id, true); }} title="Scale Up">
                  &#9654;
               </button>
            </div>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox: Not created</span>
          )}
          <button className="btn btn-delete" style={{ fontSize: '14px', padding: '4px 10px' }} onClick={(e) => { e.stopPropagation(); handleIssueDelete(issue.id); }}>&#x2715;</button>
        </div>
      </div>
      {!isCollapsed && (
        <div style={{padding: '10px'}}>
            {tasks.length > 0 ? (
                tasks.map(task => (
                    <TaskIssueCard 
                        key={task.name} 
                        task={task} 
                        repoName={repoName} 
                        issueId={issue.id}
                    />
                ))
            ) : (
                <p>No tasks found. Tasks should appear shortly if the sandbox is active.</p>
            )}
            
            <div style={{padding: '10px', borderTop: '1px solid #eee', marginTop: '10px'}}>
                <div style={{display: 'flex', gap: '10px'}}>
                    <button className="btn" onClick={() => handleCreateTask('triage-issue')}>Triage</button>
                    <button className="btn" onClick={() => handleCreateTask('address-feedback')}>Address Feedback</button>
                </div>
            </div>
        </div>
      )}
    </div>
  );
}

export default IssueCard;