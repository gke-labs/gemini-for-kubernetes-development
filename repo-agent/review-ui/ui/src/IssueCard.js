import React, { useState, useEffect } from 'react';
import yaml from 'js-yaml';
import TaskCard from './TaskCard';
import SandboxTerminal from './Terminal';

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
  handleAddIssue,
  availableModels = [],
  showToast,
}) {
  const [isCollapsed, setIsCollapsed] = useState(!isMainView);
  const [tasks, setTasks] = useState([]);
  const [iteratePrompt, setIteratePrompt] = useState('');
  const [showTerminal, setShowTerminal] = useState(false);
  const [selectedModel, setSelectedModel] = useState('');

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

  const handleCreateTask = (taskType, prompt = '', params = {}) => {
      if (!repoName || !issue.id) return;
      fetch(`/api/repo/${repoName}/issues/${issue.id}/tasks`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ taskType, prompt, params })
      })
      .then(res => {
          if (res.ok) {
              if (showToast) showToast(`Task ${taskType} started!`, 'success');
              fetchTasks();
          } else {
              res.text().then(t => { if (showToast) showToast("Failed to create task: " + t, 'error'); });
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

  if (issue.type === 'pending' || issue.type === 'excluded') {
      return (
        <div className="pr-card" style={{opacity: 0.6, border: '1px dashed var(--border-color)'}}>
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
          <a href={issue.htmlURL} target="_blank" rel="noopener noreferrer">{issue.title} (Issue #{issue.id})</a>
          {!isMainView && (
            <span style={{ marginLeft: '10px', fontSize: 'small', color: 'var(--text-secondary)' }}>
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
                    fontFamily: 'var(--font-mono)',
                    fontWeight: 'bold'
                }}
                onClick={(e) => { e.stopPropagation(); setShowTerminal(!showTerminal); }}
                title={showTerminal ? "Hide Terminal" : "Show Terminal"}
              >
                &gt;_
              </button>
              {issue.agentState === 'provisioning' ? (
                <span className="pr-sandbox" style={{backgroundColor: '#3b82f6', color: 'white', cursor: 'default', animation: 'pulse-subtle 2s ease-in-out infinite'}}>
                  Sandbox Provisioning
                </span>
              ) : (
                <a href={`/sandbox/${namespace}/${issue.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>
                  Sandbox Active
                </a>
              )}
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
          ) : getSandboxStatusClass(issue) === 'red' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>{issue.sandboxStatus || 'Error'}</span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(issue.id, true); }} title="Restart">
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
                        showToast={showToast}
                    />
                ))
            ) : (
                <p>No tasks found. Tasks should appear shortly if the sandbox is active.</p>
            )}
            
            {/* Sticky-style action bar */}
            <div style={{padding: '16px', borderTop: '1px solid var(--border-color)', marginTop: '10px'}}>
                <div style={{backgroundColor: 'var(--bg-card)', border: '1px solid var(--border-color)', borderRadius: '12px', padding: '8px', boxShadow: 'var(--shadow-card)'}}>
                    {getSandboxStatusClass(issue) === 'green' && (
                        <div>
                            <textarea
                                value={iteratePrompt}
                                onChange={(e) => setIteratePrompt(e.target.value)}
                                placeholder="Describe how to iterate on this fix..."
                                style={{width: '100%', minHeight: '50px', padding: '12px', borderRadius: '8px', border: 'none', backgroundColor: 'transparent', color: 'var(--text-primary)', fontFamily: 'var(--font-ui)', fontSize: '14px', resize: 'none', outline: 'none', boxSizing: 'border-box'}}
                            />
                        </div>
                    )}
                    <div style={{display: 'flex', alignItems: 'center', justifyContent: 'space-between', borderTop: getSandboxStatusClass(issue) === 'green' ? '1px solid var(--border-color)' : 'none', paddingTop: '8px'}}>
                        <div style={{display: 'flex', alignItems: 'center', gap: '8px'}}>
                            {availableModels && availableModels.length > 0 && (
                                <div style={{display: 'flex', alignItems: 'center', gap: '4px'}}>
                                    <span style={{fontSize: '10px', fontWeight: 700, textTransform: 'uppercase', color: 'var(--text-muted)'}}>Model:</span>
                                    <select
                                        value={selectedModel}
                                        onChange={(e) => setSelectedModel(e.target.value)}
                                        style={{padding: '4px 8px', borderRadius: '4px', border: '1px solid var(--border-color)', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)', fontSize: '11px', fontFamily: 'var(--font-ui)'}}
                                    >
                                        <option value="">Default</option>
                                        {availableModels.map(m => <option key={m} value={m}>{m}</option>)}
                                    </select>
                                </div>
                            )}
                        </div>
                        <div style={{display: 'flex', gap: '8px', flexWrap: 'wrap'}}>
                            <button className="btn btn-secondary" style={{padding: '8px 12px', fontSize: '12px', fontWeight: 700}} onClick={() => handleCreateTask('triage-issue', '', selectedModel ? { model: selectedModel } : {})}>
                                Triage
                            </button>
                            <button className="btn btn-secondary" style={{padding: '8px 12px', fontSize: '12px', fontWeight: 700}} onClick={() => {
                                const fixTask = tasks.find(t => t.type === 'fix-issue');
                                if (!fixTask || !fixTask.agentDraft) { if (showToast) showToast("No fix-issue task with draft found.", 'info'); return; }
                                const match = fixTask.agentDraft.match(/\/pull\/(\d+)/);
                                if (!match) { if (showToast) showToast("Could not extract PR ID.", 'error'); return; }
                                const params = { PULL_REQUEST_ID: match[1] };
                                if (selectedModel) params.model = selectedModel;
                                handleCreateTask('investigate-failures', '', params);
                            }}>
                                Investigate
                            </button>
                            {getSandboxStatusClass(issue) === 'green' && (
                                <button className="btn btn-submit" style={{padding: '8px 16px', fontSize: '13px'}} onClick={() => {
                                    if (!iteratePrompt.trim()) return;
                                    handleCreateTask('iterate', iteratePrompt, selectedModel ? { model: selectedModel } : {});
                                    setIteratePrompt('');
                                }}>
                                    Iterate
                                    <span className="material-symbols-outlined" style={{fontSize: '16px'}}>send</span>
                                </button>
                            )}
                        </div>
                    </div>
                </div>
            </div>
        </div>
      )}
    </div>
  );
}

export default IssueCard;