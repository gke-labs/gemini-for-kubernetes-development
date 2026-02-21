import React, { useState, useEffect, useCallback } from 'react';
import TaskCard from './TaskCard';
import SandboxTerminal from './Terminal';

function DevCard({
  sandbox,
  handleDelete,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
  handleFork,
  repoName,
}) {
  const [flairText, setFlairText] = useState('');
  const [tasks, setTasks] = useState([]);
  const [iteratePrompt, setIteratePrompt] = useState('');
  const [showTerminal, setShowTerminal] = useState(false);

  const getFlairColor = (text) => {
    if (!text) return '#cd9945ff';
    const lower = text.toLowerCase();
    if (lower.includes('ready') || lower.includes('completed') || lower.includes('submitted')) return 'green';
    if (lower.includes('running')) return 'orange';
    if (lower.includes('provisioning')) return '#2196F3';
    if (lower.includes('error') || lower.includes('failed')) return '#9e2a2aff';
    return '#cd9945ff';
  };

  const getTaskStatus = (task) => {
    if (task.taskState === 'Completed') {
         if (task.result === 'submitted') return 'Submitted';
         return 'Ready';
    }
    if (task.taskState === 'Running') return 'Running';
    if (task.taskState === 'Failed') return 'Failed';
    return task.taskState || 'Pending';
  };

  useEffect(() => {
    if (tasks.length > 0) {
        // Sort tasks by creationTimestamp descending
        const sortedTasks = [...tasks].sort((a, b) => {
            return new Date(b.creationTimestamp) - new Date(a.creationTimestamp);
        });

        let targetTask = sortedTasks[0];
        const running = sortedTasks.find(t => t.taskState === 'Running');

        if (running) targetTask = running;

        if (targetTask) {
             const status = getTaskStatus(targetTask);
             const name = targetTask.type.toUpperCase();
             setFlairText(`${name}: ${status}`);
             return;
        }
    }
    setFlairText(sandbox.agentState || '');
  }, [sandbox.agentState, tasks]);

  const fetchTasks = useCallback(() => {
    if (!repoName || !sandbox.name) return;
    fetch(`/api/repo/${encodeURIComponent(repoName)}/dev/${encodeURIComponent(sandbox.name)}/tasks`)
        .then(res => res.json())
        .then(data => {
            if (Array.isArray(data)) {
                setTasks(data);
            }
        })
        .catch(err => console.error("Failed to fetch tasks:", err));
  }, [repoName, sandbox.name]);

  const handleCreateTask = (taskType, prompt = '', params = {}) => {
      if (!repoName || !sandbox.name) return;
      fetch(`/api/repo/${encodeURIComponent(repoName)}/dev/${encodeURIComponent(sandbox.name)}/tasks`, {
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
      fetchTasks();
      const interval = setInterval(fetchTasks, 10000);
      return () => clearInterval(interval);
  }, [fetchTasks]);

  return (
    <div key={sandbox.name} className="pr-card">
      <div className="pr-card-header">
        <h3>
          <a href={sandbox.branchURL} target="_blank" rel="noopener noreferrer">{sandbox.branch || sandbox.name}</a>
        </h3>
        <div className="pr-card-actions-header">
          {sandbox.labels && sandbox.labels.length > 0 && (
            <div style={{ display: 'flex', gap: '5px', marginRight: '10px' }}>
              {sandbox.labels.map((label, index) => (
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
          {flairText && sandbox.agentState !== 'provisioning' && (
            <span 
              style={{ marginRight: '10px', backgroundColor: getFlairColor(flairText), color: 'white', padding: '5px 10px', borderRadius: '5px', fontSize: 'small' }}
              title={sandbox.agentStateMessage || ''}
            >
              {flairText}
            </span>
          )}
          {getSandboxStatusClass(sandbox) === 'green' ? (
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
              {sandbox.agentState === 'provisioning' ? (
                <span className="pr-sandbox" style={{backgroundColor: 'var(--text-link)', color: 'white', cursor: 'default'}} title={sandbox.agentStateMessage || ''}>
                  Sandbox Provisioning...
                </span>
              ) : (
                <a href={`/sandbox/${namespace}/${sandbox.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(sandbox)}`}>
                  Sandbox
                </a>
              )}
              <button className="btn btn-sm pr-sandbox yellow" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleDown(sandbox.name); }} title="Scale Down">
                &#9646;&#9646;
              </button>
            </div>
          ) : getSandboxStatusClass(sandbox) === 'yellow' ? (
             <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
               <span className={`pr-sandbox ${getSandboxStatusClass(sandbox)}`}>Sandbox</span>
               <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(sandbox.name); }} title="Scale Up">
                  &#9654;
               </button>
             </div>
          ) : getSandboxStatusClass(sandbox) === 'red' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(sandbox)}`} title={sandbox.sandboxStatus || 'Error'}>
                {sandbox.sandboxStatus?.startsWith('Evicted') ? 'Evicted' : (sandbox.sandboxStatus || 'Error')}
              </span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(sandbox.name); }} title="Restart/Reprovision Sandbox">
                  &#8635;
               </button>
            </div>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(sandbox)}`}>Sandbox: Not created</span>
          )}
           {handleFork && (
             <button 
                className="btn btn-sm" 
                style={{backgroundColor: 'var(--bg-active)', color: 'white', padding: '5px 10px'}} 
                onClick={(e) => { e.stopPropagation(); handleFork(sandbox); }} 
                                            title="Fork this approach"
                
             >
                ⑂ Fork
             </button>
           )}
           <button className="btn btn-delete" onClick={(e) => { e.stopPropagation(); handleDelete(sandbox); }}>&#x2715;</button>
        </div>
      </div>
      
      {showTerminal && getSandboxStatusClass(sandbox) === 'green' && (
        <div style={{ borderBottom: '1px solid var(--border-color)' }}>
            <SandboxTerminal namespace={namespace} sandboxName={sandbox.name} />
        </div>
      )}

      <div style={{padding: '10px'}}>
        {tasks.length > 0 && (
            tasks.slice().reverse().map((task, index) => (
                <TaskCard 
                    key={task.name} 
                    task={task} 
                    repoName={repoName} 
                    parentId={sandbox.name}
                    parentType="dev"
                    defaultCollapsed={index !== tasks.length - 1}
                />
            ))
        )}
        
        {/* Only show task creation buttons if sandbox is active (green) */}
        {getSandboxStatusClass(sandbox) === 'green' && (
            <div style={{padding: '10px', borderTop: '1px solid var(--border-color)', marginTop: '10px'}}>
                <div style={{display: 'flex', gap: '10px', flexDirection: 'column'}}>
                     <div style={{display: 'flex', gap: '5px'}}>
                        <textarea 
                            value={iteratePrompt} 
                            onChange={(e) => setIteratePrompt(e.target.value)} 
                            placeholder="Describe changes to iterate on..."
                            style={{flexGrow: 1, minHeight: '60px', padding: '5px', borderRadius: '4px', border: '1px solid var(--border-color)'}}
                        />
                        <button className="btn" onClick={() => {
                            if (!iteratePrompt.trim()) return;
                            handleCreateTask('iterate', iteratePrompt);
                            setIteratePrompt('');
                        }}>Iterate</button>
                     </div>
                     <div style={{display: 'flex', gap: '10px'}}>
                        {/* Add buttons for standard dev tasks here if needed, e.g., "Run Build", "Run Tests" */}
                        <button className="btn" onClick={() => handleCreateTask('generic-task', 'Analyze the codebase structure')}>Analyze Codebase</button>
                    </div>
                </div>
            </div>
        )}
      </div>
    </div>
  );
}

export default DevCard;
