// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// you may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
}) {
  const [isCollapsed, setIsCollapsed] = useState(!isMainView);
  const [tasks, setTasks] = useState([]);
  const [iteratePrompt, setIteratePrompt] = useState('');
  const [showTerminal, setShowTerminal] = useState(false);
  const [showRollbackUI, setShowRollbackUI] = useState(false);
  const [commits, setCommits] = useState([]);
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

  const fetchCommits = () => {
    if (!repoName || !issue.id) return;
    fetch(`/api/repo/${repoName}/issues/${issue.id}/commits`)
        .then(res => res.json())
        .then(data => {
            if (Array.isArray(data)) {
                setCommits(data);
            }
        })
        .catch(err => console.error("Failed to fetch commits:", err));
  };

  const getPRId = () => {
    let prId = "";
    const fixTask = tasks.find(t => t.type === 'fix-issue');
    if (fixTask && fixTask.agentDraft) {
        const match = fixTask.agentDraft.match(/\/pull\/(\d+)/);
        if (match) {
            prId = match[1];
        }
    }
    if (!prId && iteratePrompt) {
        const match = iteratePrompt.match(/\/pull\/(\d+)/);
        if (match) {
            prId = match[1];
        }
    }
    return prId;
  };

  const handleRollback = (sha) => {
    if (!repoName || !issue.id) return;
    const prId = getPRId();
    if (!prId) {
        alert("No PR ID found. Please ensure a 'fix-issue' task has completed with a PR link, or paste the PR link into the iteration textbox.");
        return;
    }
    if (!window.confirm(`Are you sure you want to rollback to commit ${sha.substring(0, 7)}? This will perform a force push.`)) return;

    fetch(`/api/repo/${repoName}/issues/${issue.id}/rollback`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ commitSha: sha, pullRequestId: prId })
    })
    .then(res => {
        if (res.ok) {
            alert("Rollback task created!");
            setShowRollbackUI(false);
            fetchTasks();
        } else {
            res.text().then(t => alert("Failed to rollback: " + t));
        }
    })
    .catch(err => console.error("Failed to rollback", err));
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
                <p>No tasks found. Tasks should appear shortly if the sandbox is active.</p>
            )}

            <div style={{padding: '15px', borderTop: '1px solid var(--border-color)', marginTop: '10px', backgroundColor: 'var(--bg-review-section)', borderRadius: '5px'}}>
                <h4 style={{marginTop: 0, fontSize: '14px'}}>General Issue Comment</h4>
                <textarea 
                    className="review-textarea"
                    value={drafts[issue.id] || ''} 
                    onChange={(e) => handleIssueDraftChange(issue.id, e.target.value)}
                    onBlur={() => handleIssueSaveDraft(issue.id)}
                    placeholder="Leave a general comment on this issue (not tied to a specific task)..."
                    rows={4}
                    style={{width: '100%', marginBottom: '10px'}}
                />
                <div style={{display: 'flex', justifyContent: 'flex-end'}}>
                    <button className="btn btn-submit" onClick={() => {
                        const latestTask = tasks.length > 0 ? tasks[0] : null;
                        handleIssueSubmit(issue.id, latestTask?.name, latestTask?.uid);
                    }}>Submit General Comment</button>
                </div>
            </div>
            
            <div style={{padding: '10px', borderTop: '1px solid var(--border-color)', marginTop: '10px'}}>
                <div style={{display: 'flex', gap: '10px', flexDirection: 'column'}}>
                     {getSandboxStatusClass(issue) === 'green' && (
                         <div style={{display: 'flex', gap: '5px'}}>
                            <textarea 
                                value={iteratePrompt} 
                                onChange={(e) => setIteratePrompt(e.target.value)} 
                                placeholder="Describe changes to iterate on..."
                                style={{flexGrow: 1, minHeight: '60px', padding: '5px', borderRadius: '4px', border: '1px solid var(--border-color)'}}
                            />
                            <button className="btn" onClick={() => {
                                if (!iteratePrompt.trim()) return;
                                handleCreateTask('iterate', iteratePrompt, selectedModel ? { model: selectedModel } : {});
                                setIteratePrompt('');
                            }}>Iterate</button>
                         </div>
                     )}
                    <div style={{display: 'flex', gap: '10px', alignItems: 'center'}}>
                        {availableModels && availableModels.length > 0 && (
                            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
                                <label style={{fontSize: 'small', color: 'var(--text-secondary)'}}>Model:</label>
                                <select 
                                    value={selectedModel} 
                                    onChange={(e) => setSelectedModel(e.target.value)}
                                    style={{padding: '4px', borderRadius: '4px', border: '1px solid var(--border-color)', backgroundColor: 'var(--bg-primary)', color: 'var(--text-primary)'}}
                                >
                                    <option value="">Default (All)</option>
                                    {availableModels.map(m => <option key={m} value={m}>{m}</option>)}
                                </select>
                            </div>
                        )}
                        <button className="btn" onClick={() => handleCreateTask('triage-issue', '', selectedModel ? { model: selectedModel } : {})}>Triage</button>
                        {!showRollbackUI && (
                            <button className="btn" style={{backgroundColor: 'var(--status-grey)'}} onClick={() => { setShowRollbackUI(true); fetchCommits(); }}>Rollback to previous commit</button>
                        )}
                        <button className="btn" onClick={() => {
                            const prId = getPRId();

                            if (!prId) {
                                alert("No PR ID found. Please ensure a 'fix-issue' task has completed with a PR link, or paste the PR link into the iteration textbox.");
                                return;
                            }
                            const params = { PULL_REQUEST_ID: prId };
                            if (selectedModel) params.model = selectedModel;
                            handleCreateTask('address-feedback', '', params);
                        }}>Address Feedback</button>
                        <button className="btn" onClick={() => {
                            const prId = getPRId();

                            if (!prId) {
                                alert("No PR ID found. Please ensure a 'fix-issue' task has completed with a PR link, or paste the PR link into the iteration textbox.");
                                return;
                            }
                            const params = { PULL_REQUEST_ID: prId };
                            if (selectedModel) params.model = selectedModel;
                            handleCreateTask('investigate-failures', '', params);
                        }}>Investigate Failures</button>
                    </div>
                    {showRollbackUI && (
                        <div className="new-task-form" style={{padding: '10px', backgroundColor: 'var(--bg-secondary)', borderRadius: '5px'}}>
                            <h4>Rollback to Previous Commit</h4>
                            <p style={{fontSize: 'small', color: 'var(--text-secondary)', marginBottom: '10px'}}>
                                Select a commit to rollback the issue branch to. This will perform a <strong>force push</strong>.
                            </p>
                            <div style={{maxHeight: '300px', overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: '4px', backgroundColor: 'var(--bg-primary)'}}>
                                {commits.length === 0 ? (
                                    <div style={{padding: '10px', textAlign: 'center'}}>Loading commits...</div>
                                ) : (
                                    commits.map((commit) => (
                                        <div 
                                            key={commit.sha} 
                                            style={{
                                                padding: '10px', 
                                                borderBottom: '1px solid var(--border-color)', 
                                                display: 'flex', 
                                                justifyContent: 'space-between', 
                                                alignItems: 'center',
                                                cursor: 'pointer'
                                            }}
                                            onClick={() => handleRollback(commit.sha)}
                                            onMouseOver={(e) => e.currentTarget.style.backgroundColor = 'var(--bg-hover)'}
                                            onMouseOut={(e) => e.currentTarget.style.backgroundColor = ''}
                                        >
                                            <div style={{overflow: 'hidden'}}>
                                                <div style={{fontWeight: 'bold', fontSize: 'small', textOverflow: 'ellipsis', whiteSpace: 'nowrap', overflow: 'hidden'}} title={commit.message}>
                                                    {commit.message}
                                                </div>
                                                <div style={{fontSize: 'x-small', color: 'var(--text-secondary)'}}>
                                                    {commit.author} on {new Date(commit.date).toLocaleString()}
                                                </div>
                                            </div>
                                            <div style={{fontFamily: 'monospace', fontSize: 'x-small', backgroundColor: 'var(--bg-secondary)', padding: '2px 4px', borderRadius: '3px', marginLeft: '10px'}}>
                                                {commit.sha.substring(0, 7)}
                                            </div>
                                        </div>
                                    ))
                                )}
                            </div>
                            <div style={{marginTop: '10px'}}>
                                <button className="btn" style={{backgroundColor: 'var(--status-grey)'}} onClick={() => setShowRollbackUI(false)}>Cancel</button>
                            </div>
                        </div>
                    )}
                </div>
            </div>
        </div>
      )}
    </div>
  );
}

export default IssueCard;