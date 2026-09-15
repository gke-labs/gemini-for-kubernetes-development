import React, { useState, useEffect } from 'react';
import TaskCard from './TaskCard';
import SandboxTerminal from './Terminal';

// Issue fixes run through the factory CLI: the backend exposes one
// synthesized task per issue (state from the fix sandbox), a PR link once the
// fix opened one (issue.branchURL), and a re-fix trigger (POST .../tasks).
function IssueCard({
  issue,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
  handleIssueDelete,
  repoName,
  isMainView,
  handleAddIssue,
}) {
  const [isCollapsed, setIsCollapsed] = useState(!isMainView);
  const [tasks, setTasks] = useState([]);
  const [showTerminal, setShowTerminal] = useState(false);

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

  const handleFixAgain = () => {
      if (!repoName || !issue.id) return;
      fetch(`/api/repo/${repoName}/issues/${issue.id}/tasks`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ taskType: 'fix' })
      })
      .then(res => {
          if (res.ok) {
              alert("Re-fix requested! The fix task will relaunch shortly.");
              fetchTasks();
          } else {
              res.text().then(t => alert("Failed to request re-fix: " + t));
          }
      })
      .catch(err => console.error("Failed to request re-fix", err));
  };

  useEffect(() => {
    if (isMainView || !isCollapsed) {
        fetchTasks();
        const interval = setInterval(fetchTasks, 10000);
        return () => clearInterval(interval);
    }
  }, [isCollapsed, isMainView, repoName, issue.id]);

  if (issue.type === 'pending' || issue.type === 'excluded') {
      return (
        <div className="pr-card" style={{opacity: 0.6, border: '1px dashed #ccc'}}>
             <div className="pr-card-header" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '10px 20px'}}>
                <h3 style={{margin: 0}}>
                  {issue.htmlURL ? (
                    <a href={issue.htmlURL} target="_blank" rel="noopener noreferrer" style={{color: 'inherit', textDecoration: 'none'}}>
                      {issue.title}
                    </a>
                  ) : (
                    issue.title
                  )}
                </h3>
                <button
                  className="btn"
                  onClick={(e) => {
                      e.stopPropagation();
                      if (handleAddIssue) handleAddIssue(issue.id);
                  }}
                  title="Add to watch list"
                  style={{fontSize: '20px', width: '40px', height: '40px', borderRadius: '20px', lineHeight: '20px', display: 'flex', alignItems: 'center', justifyContent: 'center'}}
                >
                  +
                </button>
             </div>
        </div>
      );
  }

  return (
    <div key={issue.id} className="pr-card">
      <div className="pr-card-header" onClick={() => !isMainView && setIsCollapsed(!isCollapsed)} style={isMainView ? {cursor: 'default'} : {}}>
        <h3>
          <a href={issue.htmlURL} target="_blank" rel="noopener noreferrer">{issue.title || `Issue #${issue.id}`} (Issue #{issue.id})</a>
          {!isMainView && (
            <span style={{ marginLeft: '10px', fontSize: 'small', color: 'var(--text-secondary)' }}>
                {isCollapsed ? 'click to expand' : 'click to collapse'}
            </span>
          )}
        </h3>
        <div className="pr-card-actions-header">
          {issue.branchURL && (
            <a
              href={issue.branchURL}
              target="_blank"
              rel="noopener noreferrer"
              className="pr-sandbox green"
              style={{marginRight: '10px'}}
              title="Pull request opened by the fix task"
            >
              View PR
            </a>
          )}
          {issue.labels && issue.labels.length > 0 && (
            <div style={{ display: 'flex', gap: '5px', marginRight: '10px' }}>
              {issue.labels.map((label, index) => (
                <span
                  key={index}
                  style={{
                    backgroundColor: 'var(--bg-secondary)',
                    color: 'var(--text-primary)',
                    padding: '2px 6px',
                    borderRadius: '4px',
                    fontSize: 'small',
                    border: '1px solid var(--border-color)'
                  }}
                >
                  {label}
                </span>
              ))}
            </div>
          )}
          {getSandboxStatusClass(issue) === 'green' ? (
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
              {issue.agentState === 'provisioning' ? (
                <span className="pr-sandbox" style={{backgroundColor: '#2196F3', color: 'white', cursor: 'default'}}>
                  Sandbox Provisioning
                </span>
              ) : (
                <a href={`/sandbox/${namespace}/${issue.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>
                  Sandbox Active
                </a>
              )}
               <button className="btn btn-sm pr-sandbox yellow" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleDown(issue.id); }} title="Pause / Scale Down">
                &#9646;&#9646;
              </button>
            </div>
          ) : getSandboxStatusClass(issue) === 'yellow' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox Paused</span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(issue.id, true); }} title="Unpause / Scale Up">
                  &#9654;
               </button>
            </div>
          ) : getSandboxStatusClass(issue) === 'red' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`} title={issue.sandboxStatus || 'Error'}>
                {issue.sandboxStatus?.startsWith('Evicted') ? 'Evicted' : (issue.sandboxStatus || 'Error')}
              </span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(issue.id, true); }} title="Restart/Reprovision Sandbox">
                  &#8635;
               </button>
            </div>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox: Not created</span>
          )}
          <button className="btn btn-delete" style={{ fontSize: '14px', padding: '4px 10px' }} onClick={(e) => { e.stopPropagation(); handleIssueDelete(issue.id); }}>&#x2715;</button>
        </div>
      </div>

      {showTerminal && getSandboxStatusClass(issue) === 'green' && (
        <div style={{ borderBottom: '1px solid var(--border-color)' }}>
            <SandboxTerminal namespace={namespace} sandboxName={issue.sandbox} />
        </div>
      )}

      {!isCollapsed && (
        <div style={{padding: '10px'}}>
            {tasks.length > 0 ? (
                tasks.slice().reverse().map((task, index) => (
                    <TaskCard
                        key={task.name}
                        task={task}
                        repoName={repoName}
                        parentId={issue.id}
                        parentType="issues"
                        defaultCollapsed={index !== tasks.length - 1}
                    />
                ))
            ) : (
                <p>No fix task yet. One should appear shortly once the issue is picked up.</p>
            )}

            <div style={{padding: '10px', borderTop: '1px solid var(--border-color)', marginTop: '10px'}}>
                <div style={{display: 'flex', gap: '10px', alignItems: 'center'}}>
                    <button className="btn" onClick={handleFixAgain} title="Relaunch the fix task for this issue">Fix Again</button>
                    {issue.branchURL && (
                        <span style={{fontSize: 'small', color: 'var(--text-secondary)'}}>
                            CI failures and review comments on the PR are followed up automatically.
                        </span>
                    )}
                </div>
            </div>
        </div>
      )}
    </div>
  );
}

export default IssueCard;
