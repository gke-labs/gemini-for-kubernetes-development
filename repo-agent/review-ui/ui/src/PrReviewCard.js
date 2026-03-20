import React, { useState, useEffect, useRef } from 'react';
import yaml from 'js-yaml';
import { parseDiff, Diff, getChangeKey } from 'react-diff-view';
import 'react-diff-view/style/index.css';


function TaskReviewCard({
    task,
    prId,
    drafts,
    reviewViewModes,
    yamlDrafts,
    handleSaveDraft,
    handleDraftChange,
    handleRemoveComment,
    toggleReviewView,
    handleYamlDraftChange,
    handleYamlDraftBlur,
    handleSubmit,
    handleExportCurl,
    namespace,
    isSubmitted,
    diff,
    diffError,
    fileCollapsed,
    setFileCollapsed,
    handleMoveCommentAndSave,
    lastDragTargetRef,
    setCurlCommand,
    curlCommand,
    handleSubmitTask,
    handleSaveTaskDraft,
    prUrl,
    repoName
}) {
    const [taskCollapsed, setTaskCollapsed] = useState(false);
    const [showLogs, setShowLogs] = useState(false);
    const [logs, setLogs] = useState('');
    const [reviewFlairText, setReviewFlairText] = useState('');
    const [localYaml, setLocalYaml] = useState(task.userDraft || task.agentDraft || '');
    // Parse initial YAML to structured object for Diff/Form views
    const [localDraft, setLocalDraft] = useState(() => {
        try {
            return yaml.load(task.userDraft || task.agentDraft || '') || {};
        } catch (e) {
            return {};
        }
    });

    useEffect(() => {
        let isMounted = true;
        let timeoutId;

        if (showLogs && repoName) {
            const fetchLogs = () => {
                fetch(`/api/repo/${encodeURIComponent(repoName)}/prs/${encodeURIComponent(prId)}/tasks/${encodeURIComponent(task.name)}/logs`)
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
    }, [showLogs, repoName, prId, task.name]);

    // Update local state when task prop updates (e.g. re-fetch)
    useEffect(() => {
         const content = task.userDraft || task.agentDraft || '';
         if (content !== localYaml) {
             setLocalYaml(content);
             try {
                 setLocalDraft(yaml.load(content) || {});
             } catch (e) {
                 // ignore parse error on init
             }
         }
    }, [task.userDraft, task.agentDraft]);

    const handleLocalYamlChange = (val) => {
        setLocalYaml(val);
        // debounce parse or parse on blur? For now, try parse immediately for responsiveness
        try {
            setLocalDraft(yaml.load(val) || {});
        } catch (e) {
            // invalid yaml, don't update structured view yet
        }
    };

    const handleLocalStructuredChange = (path, value, index) => {
        // Create deep copy
        const newDraft = JSON.parse(JSON.stringify(localDraft));
        
        // Helper to set value by path string "review.body" etc
        // path is like 'note', 'review.body', 'comment.body' (with index)
        if (path === 'note') {
            newDraft.note = value;
        } else if (path === 'review.body') {
            if (!newDraft.review) newDraft.review = {};
            newDraft.review.body = value;
        } else if (path === 'comment.body' && index !== undefined) {
             if (newDraft.review && newDraft.review.comments && newDraft.review.comments[index]) {
                 newDraft.review.comments[index].body = value;
             }
        }
        
        setLocalDraft(newDraft);
        // update YAML
        try {
            setLocalYaml(yaml.dump(newDraft));
        } catch (e) {
            console.error("Failed to dump yaml", e);
        }
    };

    const handleLocalRemoveComment = (index) => {
        const newDraft = JSON.parse(JSON.stringify(localDraft));
        if (newDraft.review && newDraft.review.comments) {
            newDraft.review.comments.splice(index, 1);
            setLocalDraft(newDraft);
            try {
                const newYaml = yaml.dump(newDraft);
                setLocalYaml(newYaml);
                if (handleSaveTaskDraft) {
                    handleSaveTaskDraft(task.name, newYaml);
                }
            } catch (e) {
                console.error("Failed to dump yaml", e);
            }
        }
    };

    const handleLocalMoveComment = (index, path, line, side) => {
        const newDraft = JSON.parse(JSON.stringify(localDraft));
        if (newDraft.review && newDraft.review.comments && newDraft.review.comments[index]) {
            const comment = newDraft.review.comments[index];
            comment.path = path;
            comment.line = line;
            comment.side = side;
            
            setLocalDraft(newDraft);
            try {
                const newYaml = yaml.dump(newDraft);
                setLocalYaml(newYaml);
                if (handleSaveTaskDraft) {
                    handleSaveTaskDraft(task.name, newYaml);
                }
            } catch (e) {
                console.error("Failed to dump yaml", e);
            }
        }
    };
    
    // We need a way to save this specific task's draft to the backend
    const saveTaskDraft = () => {
        // Call API to update task userDraft
        // We need the parent to pass a handler or call fetch directly here.
        // Let's assume we pass a new prop `handleSaveTaskDraft`
        if (handleSaveTaskDraft) {
            handleSaveTaskDraft(task.name, localYaml);
        }
    };
    
    // Custom submit that uses local content
    const submitTaskDraft = () => {
         // We need to tell the parent to submit THIS content
         if (handleSubmitTask) {
             handleSubmitTask(prId, localYaml);
         } else {
             // Fallback: update global draft then submit?
             // Or call the existing handleSubmit but we need it to support payload override.
             // Let's assume handleSubmit can take content.
             handleSubmit(prId, localYaml);
         }
    };

    const getReviewFlairColor = (flairText) => {
        if (!flairText) return '#3e7f67ff';
        const text = flairText.toLowerCase();
        if (text === 'done' || text === 'review ready' || text === 'completed') return 'green';
        if (text.includes('reviewing') || text === 'running') return 'orange';
        if (text.includes('error') || text === 'failed') return '#9e2a2aff';
        if (text === 'submitted' || text === 'review draft created') return '#3f5398ff';
        return '#cd9945ff'; // Default color
    };

    useEffect(() => {
        if (task.taskState === 'Completed') {
             if (isSubmitted) {
                 setReviewFlairText('Review Draft Created');
             } else {
                 setReviewFlairText('Ready');
             }
        } else if (task.taskState === 'Running') {
             setReviewFlairText('Running Task');
        } else if (task.taskState === 'Failed') {
             setReviewFlairText('Task Failed');
        } else {
             setReviewFlairText(task.taskState || task.agentState || 'Pending');
        }
    }, [task, isSubmitted]);

    const renderDiffView = () => {
        if (diffError) {
          return <div className="diff-container error">Could not load diff: {diffError}</div>;
        }
        if (!diff) {
          return <div className="diff-container">Loading diff...</div>;
        }
    
        const comments = localDraft?.review?.comments || [];
        const indexedComments = comments.map((c, i) => ({ ...c, index: i }));
    
        return (
          <div className="diff-container">
            <h4>Diff</h4>
            {diff.map(({ oldRevision, newRevision, type, hunks, newPath, oldPath }) => {
              const path = newPath !== '/dev/null' ? newPath : oldPath;
              const fileComments = indexedComments.filter(c => c.path === path);
              const allChanges = hunks.reduce((acc, hunk) => [...acc, ...hunk.changes], []);
    
              const commentsByChangeKey = {};
              const placedComments = new Set();
    
              fileComments.forEach(comment => {
                const { line, side, index } = comment;
    
                if (!line) {
                  return;
                }
    
                const targetChange = allChanges.find(change => {
                  if (side === 'RIGHT') {
                    if (change.type === 'insert') {
                      return line === change.lineNumber;
                    }
                    if (change.type === 'normal') {
                      return line === change.newLineNumber;
                    }
                  } else if (side === 'LEFT') {
                    if (change.type === 'delete') {
                      return line === change.lineNumber;
                    }
                    if (change.type === 'normal') {
                      return line === change.oldLineNumber;
                    }
                  }
                  return false;
                });
    
                if (targetChange) {
                  const changeKey = getChangeKey(targetChange);
                  if (!commentsByChangeKey[changeKey]) {
                    commentsByChangeKey[changeKey] = [];
                  }
                  commentsByChangeKey[changeKey].push(comment);
                  placedComments.add(index);
                }
              });
    
              const unplacedComments = fileComments.filter(comment => !placedComments.has(comment.index));
    
              const widgets = {};
              for (const changeKey in commentsByChangeKey) {
                const keyComments = commentsByChangeKey[changeKey];
                widgets[changeKey] = (
                  <div className="diff-widget">
                    {keyComments.map(comment => (
                      <div
                        key={comment.index}
                        draggable={!isSubmitted}
                        onDragStart={e => {
                          if (isSubmitted) return;
                          e.dataTransfer.setData('application/json', JSON.stringify({ prId: prId, commentIndex: comment.index }));
                          e.stopPropagation();
                        }}
                        style={{ cursor: isSubmitted ? 'default' : 'move' }}
                      >
                        {isSubmitted ? (
                          <pre className="review-pre">{comment.body}</pre>
                        ) : (
                          <>
                            <textarea
                              className="review-textarea"
                              value={comment.body || ''}
                              onChange={(e) => handleLocalStructuredChange('comment.body', e.target.value, comment.index)}
                              onBlur={saveTaskDraft}
                              placeholder="Line-specific comment..."
                            ></textarea>
                            <button className="btn btn-remove-comment" onClick={() => handleLocalRemoveComment(comment.index)}>Remove</button>
                          </>
                        )}
                      </div>
                    ))}
                  </div>
                );
              }
    
              const fileId = oldRevision + '-' + newRevision;
              const isFileCollapsed = fileCollapsed[fileId];
    
              const toggleFileCollapse = () => {
                setFileCollapsed(prevState => ({
                  ...prevState,
                  [fileId]: !prevState[fileId]
                }));
              };
    
              const handleDragOverFile = e => {
                e.preventDefault();
                let target = e.target;
                while (target && !target.classList.contains('diff-line')) {
                  target = target.parentElement;
                }
    
                if (lastDragTargetRef.current !== target) {
                  if (lastDragTargetRef.current) {
                    lastDragTargetRef.current.style.backgroundColor = '';
                  }
                  if (target) {
                    target.style.backgroundColor = 'rgba(0, 100, 255, 0.1)';
                  }
                  lastDragTargetRef.current = target;
                }
              };
    
              const handleDragLeaveFile = e => {
                if (lastDragTargetRef.current && !e.currentTarget.contains(e.relatedTarget)) {
                  lastDragTargetRef.current.style.backgroundColor = '';
                  lastDragTargetRef.current = null;
                }
              };
    
              const handleDrop = (e) => {
                e.preventDefault();
                e.stopPropagation();
                
                if (lastDragTargetRef.current) {
                  lastDragTargetRef.current.style.backgroundColor = '';
                  lastDragTargetRef.current = null;
                }
    
                const commentDataText = e.dataTransfer.getData('application/json');
                if (!commentDataText) return;
                const commentData = JSON.parse(commentDataText);
                const { prId: droppedPrId, commentIndex } = commentData;
                
                if (droppedPrId !== prId) return;

                let target = e.target;
                while (target && !target.classList.contains('diff-line')) {
                    target = target.parentElement;
                }
    
                if (!target) return;
    
                const gutters = target.querySelectorAll('.diff-gutter');
                let oldLineGutter = gutters[0];
                let newLineGutter = gutters[1];
    
                if (gutters.length === 1) {
                  if (type === 'add') {
                    newLineGutter = gutters[0];
                    oldLineGutter = undefined;
                  } else if (type === 'delete') {
                    oldLineGutter = gutters[0];
                    newLineGutter = undefined;
                  }
                }
    
                const rect = target.getBoundingClientRect();
                let isRightSide = e.clientX > rect.left + rect.width / 2;
    
                if (type === 'add') {
                  isRightSide = true;
                } else if (type === 'delete') {
                  isRightSide = false;
                }
    
                const side = isRightSide ? 'RIGHT' : 'LEFT';
    
                let line;
                if (side === 'RIGHT') {
                    const newLineNumber = parseInt(newLineGutter?.textContent, 10);
                    if (!isNaN(newLineNumber)) {
                        line = newLineNumber;
                    }
                } else { // LEFT
                    const oldLineNumber = parseInt(oldLineGutter?.textContent, 10);
                    if (!isNaN(oldLineNumber)) {
                        line = oldLineNumber;
                    }
                }
    
                if (line && side) {
                    handleLocalMoveComment(commentIndex, path, line, side);
                }
              };

              const handleLineClick = (e) => {
                if (isSubmitted) return;
                if (e.target.closest('.diff-widget')) return;

                let target = e.target;
                while (target && !target.classList.contains('diff-line')) {
                    target = target.parentElement;
                }
    
                if (!target) return;
    
                const gutters = target.querySelectorAll('.diff-gutter');
                let oldLineGutter = gutters[0];
                let newLineGutter = gutters[1];
    
                if (gutters.length === 1) {
                  if (type === 'add') {
                    newLineGutter = gutters[0];
                    oldLineGutter = undefined;
                  } else if (type === 'delete') {
                    oldLineGutter = gutters[0];
                    newLineGutter = undefined;
                  }
                }
    
                const rect = target.getBoundingClientRect();
                let isRightSide = e.clientX > rect.left + rect.width / 2;
    
                if (type === 'add') {
                  isRightSide = true;
                } else if (type === 'delete') {
                  isRightSide = false;
                }
    
                const side = isRightSide ? 'RIGHT' : 'LEFT';
    
                let line;
                if (side === 'RIGHT') {
                    const newLineNumber = parseInt(newLineGutter?.textContent, 10);
                    if (!isNaN(newLineNumber)) {
                        line = newLineNumber;
                    }
                } else { // LEFT
                    const oldLineNumber = parseInt(oldLineGutter?.textContent, 10);
                    if (!isNaN(oldLineNumber)) {
                        line = oldLineNumber;
                    }
                }
    
                if (line && side) {
                     const newDraft = JSON.parse(JSON.stringify(localDraft));
                     if (!newDraft.review) newDraft.review = {};
                     if (!newDraft.review.comments) newDraft.review.comments = [];
                     
                     newDraft.review.comments.push({
                         path: path,
                         side: side,
                         line: line,
                         body: ''
                     });
                     
                     setLocalDraft(newDraft);
                     try {
                         const newYaml = yaml.dump(newDraft);
                         setLocalYaml(newYaml);
                         if (handleSaveTaskDraft) {
                             handleSaveTaskDraft(task.name, newYaml);
                         }
                     } catch (e) {
                         console.error("Failed to dump yaml", e);
                     }
                }
              };
    
              return (
                <div key={fileId} className="diff-file">
                  <div className="diff-file-header" onClick={toggleFileCollapse} style={{ cursor: 'pointer', display: 'flex', alignItems: 'center' }}>
                    {path}
                    {fileComments.length > 0 && (
                      <span style={{ marginLeft: '10px', backgroundColor: 'orange', borderRadius: '50%', width: '20px', height: '20px', display: 'flex', justifyContent: 'center', alignItems: 'center', color: 'white', fontSize: 'small' }}>
                        {fileComments.length}
                      </span>
                    )}
                    <span style={{ marginLeft: '10px', fontSize: 'small', color: 'var(--text-secondary)' }}>
                      {isFileCollapsed ? 'click to expand' : 'click to collapse'}
                    </span>
                  </div>
                  {!isFileCollapsed && (
                    <div onDragOver={handleDragOverFile} onDrop={handleDrop} onDragLeave={handleDragLeaveFile} onClick={handleLineClick}>
                      {unplacedComments.length > 0 && (
                        <div className="diff-widget" style={{padding: '10px', borderBottom: '1px solid var(--border-color)'}}>
                          <h6>Comments on lines not shown in diff or file-level comments</h6>
                          {unplacedComments.map(comment => (
                            <div
                              key={comment.index}
                              style={{ borderTop: '1px solid var(--border-color)', paddingTop: '5px', marginTop: '5px', cursor: isSubmitted ? 'default' : 'move' }}
                              draggable={!isSubmitted}
                              onDragStart={e => {
                                  if (isSubmitted) return;
                                  e.dataTransfer.setData('application/json', JSON.stringify({ prId: prId, commentIndex: comment.index }));
                                  e.stopPropagation();
                              }}
                            >
                              {isSubmitted ? (
                                <>
                                  {comment.line && <p style={{fontSize: 'small', color: 'var(--text-secondary)', marginBottom: '5px'}}>Line: {comment.line} ({comment.side || 'RIGHT'})</p>}
                                  <pre className="review-pre">{comment.body}</pre>
                                </>
                              ) : (
                                <>
                                  {comment.line && <p style={{fontSize: 'small', color: 'var(--text-secondary)', marginBottom: '5px'}}>Line: {comment.line} ({comment.side || 'RIGHT'})</p>}
                                  <textarea
                                    className="review-textarea"
                                    value={comment.body || ''}
                                    onChange={(e) => handleLocalStructuredChange('comment.body', e.target.value, comment.index)}
                                    onBlur={saveTaskDraft}
                                    placeholder="Comment..."
                                  ></textarea>
                                  <button className="btn btn-remove-comment" onClick={() => handleLocalRemoveComment(comment.index)}>Remove</button>
                                </>
                              )}
                            </div>
                          ))}
                        </div>
                      )}
                      <Diff viewType="split" diffType={type} hunks={hunks} widgets={widgets} />
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        );
      };

    return (
        <div style={{border: '1px solid var(--border-color)', borderRadius: '5px', margin: '10px 0', backgroundColor: 'var(--bg-review-section)'}}>
            <div 
                style={{padding: '10px', borderBottom: '1px solid var(--border-color)', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer', backgroundColor: 'var(--bg-hover)'}}
                onClick={() => setTaskCollapsed(!taskCollapsed)}
            >
                <div>
                    <strong>{task.type.toUpperCase()}</strong> - {new Date(task.creationTimestamp).toLocaleString()}
                    <span style={{ marginLeft: '10px', fontSize: 'small', color: 'var(--text-secondary)' }}>
                        {taskCollapsed ? 'click to expand' : 'click to collapse'}
                    </span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                    {task.stats?.models && (() => {
                        const totalReqs = Object.values(task.stats.models).reduce((sum, m) => sum + (m.totalRequests || 0), 0);
                        return totalReqs > 0 ? (
                            <span style={{ fontSize: 'small', color: 'var(--text-secondary)', fontFamily: 'monospace' }}>
                                {totalReqs} {totalReqs === 1 ? 'req' : 'reqs'}
                            </span>
                        ) : null;
                    })()}
                    {reviewFlairText && (
                        <span
                        style={{ backgroundColor: getReviewFlairColor(reviewFlairText), color: 'white', padding: '5px 10px', borderRadius: '5px', fontSize: 'small' }}
                        title={task.agentStateMessage || ''}
                        >
                        {reviewFlairText}
                        </span>
                    )}
                </div>
            </div>
            
            {!taskCollapsed && (
                <div style={{padding: '15px'}}>
                     <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '10px 0', gap: '10px' }}>
                        <button className="btn" onClick={() => setShowLogs(!showLogs)}>
                            {showLogs ? 'Hide Logs' : 'View Logs'}
                        </button>
                        <button className="btn" onClick={() => toggleReviewView(prId)}>
                        {reviewViewModes[prId] === 'structured' ? 'View as YAML' : 'View as Structured'}
                        </button>
                    </div>
                     {showLogs && (
                        <div className="logs-display" style={{backgroundColor: '#333', color: '#fff', padding: '10px', borderRadius: '5px', marginBottom: '10px', maxHeight: '300px', overflowY: 'auto'}}>
                            <pre style={{margin: 0, whiteSpace: 'pre-wrap', fontFamily: 'monospace'}}>{logs || 'Loading logs...'}</pre>
                        </div>
                     )}
                     {task.stats?.models && Object.keys(task.stats.models).length > 0 && (
                        <div style={{ marginBottom: '10px', border: '1px solid var(--border-color)', borderRadius: '5px', overflow: 'hidden' }}>
                            <div style={{ padding: '6px 10px', backgroundColor: 'var(--bg-secondary)', borderBottom: '1px solid var(--border-color)', fontSize: 'small', fontWeight: 'bold' }}>
                                Model Usage
                            </div>
                            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'small', fontFamily: 'monospace' }}>
                                <thead>
                                    <tr style={{ backgroundColor: 'var(--bg-secondary)', textAlign: 'right' }}>
                                        <th style={{ padding: '6px 10px', textAlign: 'left', borderBottom: '1px solid var(--border-color)' }}>Model</th>
                                        <th style={{ padding: '6px 10px', borderBottom: '1px solid var(--border-color)' }}>Reqs</th>
                                        <th style={{ padding: '6px 10px', borderBottom: '1px solid var(--border-color)' }}>Input</th>
                                        <th style={{ padding: '6px 10px', borderBottom: '1px solid var(--border-color)' }}>Output</th>
                                        <th style={{ padding: '6px 10px', borderBottom: '1px solid var(--border-color)' }}>Thinking</th>
                                        <th style={{ padding: '6px 10px', borderBottom: '1px solid var(--border-color)' }}>Total</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {Object.entries(task.stats.models).map(([model, usage]) => (
                                        <tr key={model}>
                                            <td style={{ padding: '6px 10px', borderBottom: '1px solid var(--border-color)' }}>{model}</td>
                                            <td style={{ padding: '6px 10px', textAlign: 'right', borderBottom: '1px solid var(--border-color)' }}>{(usage.totalRequests || 0).toLocaleString()}</td>
                                            <td style={{ padding: '6px 10px', textAlign: 'right', borderBottom: '1px solid var(--border-color)' }}>{(usage.inputTokens || 0).toLocaleString()}</td>
                                            <td style={{ padding: '6px 10px', textAlign: 'right', borderBottom: '1px solid var(--border-color)' }}>{(usage.outputTokens || 0).toLocaleString()}</td>
                                            <td style={{ padding: '6px 10px', textAlign: 'right', borderBottom: '1px solid var(--border-color)' }}>{(usage.thoughtTokens || 0).toLocaleString()}</td>
                                            <td style={{ padding: '6px 10px', textAlign: 'right', borderBottom: '1px solid var(--border-color)' }}>{(usage.totalTokens || 0).toLocaleString()}</td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </div>
                     )}
                     {isSubmitted ? (
                        reviewViewModes[prId] === 'structured' ? (
                        <div className="review-display">
                            <strong>Review:</strong>
                            {localDraft?.note &&
                            <div className="review-section">
                                <h4>Note to Reviewer</h4>
                                <pre className="review-pre">{localDraft.note}</pre>
                            </div>
                            }
                            {localDraft?.review?.body &&
                            <div className="review-section">
                                <h4>GitHub Review</h4>
                                <pre className="review-pre">{localDraft.review.body}</pre>
                            </div>
                            }
                        </div>
                        ) : (
                        <div className="review-display">
                            <strong>Review:</strong>
                            <pre>{localYaml || ''}</pre>
                        </div>
                        )
                    ) : (
                        reviewViewModes[prId] === 'structured' ? (
                        <div className="review-form">
                            <div className="review-section">
                            <h4>Note to Reviewer</h4>
                            <textarea
                                className="review-textarea"
                                value={localDraft?.note || ''}
                                onChange={(e) => handleLocalStructuredChange('note', e.target.value)}
                                onBlur={saveTaskDraft}
                                placeholder="A description of the changes as a note to the reviewer..."
                            ></textarea>
                            </div>
                            <div className="review-section">
                            <h4>GitHub Review</h4>
                            <textarea
                                className="review-textarea"
                                value={localDraft?.review?.body || ''}
                                onChange={(e) => handleLocalStructuredChange('review.body', e.target.value)}
                                onBlur={saveTaskDraft}
                                placeholder="Overall review comment for the PR..."
                            ></textarea>
                            </div>
                        </div>
                        ) : (
                        <div className="review-form">
                            <div className="review-section">
                            <h4>Review YAML</h4>
                            <textarea
                                className="review-textarea yaml-editor"
                                style={{ height: '300px', fontFamily: 'monospace' }}
                                value={localYaml || ''}
                                onChange={(e) => handleLocalYamlChange(e.target.value)}
                                onBlur={saveTaskDraft}
                                placeholder="Enter review as YAML..."
                            ></textarea>
                            </div>
                        </div>
                        )
                    )}
                    {renderDiffView()}
                    <div className="pr-card-actions">
                        {!isSubmitted && (
                        <button className="btn btn-submit" onClick={() => submitTaskDraft()}>
                            Create Draft Review
                        </button>
                        )}
                        {isSubmitted && (
                        <a href={prUrl} target="_blank" rel="noopener noreferrer" className="btn btn-submit" style={{textDecoration: 'none'}}>
                            Go to review
                        </a>
                        )}
                        <button className="btn btn-submit" style={{marginLeft: '10px', backgroundColor: 'var(--status-grey)'}} onClick={() => handleExportCurl(prId, setCurlCommand)} disabled={isSubmitted}>
                        Export Curl Command
                        </button>
                    </div>
                </div>
            )}
        </div>
    );
}

function PrReviewCard({
  pr,
  drafts,
  collapsedReviews,
  reviewViewModes,
  yamlDrafts,
  handleDelete,
  handleSaveDraft,
  handleDraftChange,
  handleRemoveComment,
  toggleReviewView,
  handleYamlDraftChange,
  handleYamlDraftBlur,
  handleSubmit,
  handleExportCurl,
  toggleCollapse,
  getSandboxStatusClass,
  namespace,
  handleMoveCommentAndSave,
  handleScaleUp,
  handleScaleDown,
  handleAddPR,
  isMainView,
  lastUpdated,
  repoName: propRepoName,
  onRefresh,
  availableModels = [],
}) {
  const [diff, setDiff] = useState(null);
  const [diffError, setDiffError] = useState(null);
  const [fileCollapsed, setFileCollapsed] = useState({});
  const [curlCommand, setCurlCommand] = useState(null);
  const [tasks, setTasks] = useState([]);
  const [showNewTaskForm, setShowNewTaskForm] = useState(false);
  const [newTaskPrompt, setNewTaskPrompt] = useState('');
  const [expectedComments, setExpectedComments] = useState(0);
  const [selectedModel, setSelectedModel] = useState('gemini-3.1-pro-preview');
  const lastDragTargetRef = useRef(null);

  const reviewModels = (availableModels && availableModels.length > 0) ? availableModels : [
    'gemini-3.1-pro-preview',
    'gemini-3-flash-preview',
    'gemini-3.1-flash-lite-preview',
    'gemini-2.5-pro',
    'gemini-2.5-flash',
    'gemini-2.5-flash-lite'
  ];


  const isCollapsed = collapsedReviews[pr.id];
  const repoName = propRepoName || (pr.sandbox ? pr.sandbox.split('-pr-')[0] : '');

  const handleSaveTaskDraft = (taskName, draft) => {
      if (!repoName) return;
      fetch(`/api/repo/${repoName}/tasks/${taskName}/draft`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ draft })
      }).catch(err => console.error("Failed to save task draft", err));
  };

  const handleSubmitTask = (prId, draft) => {
      if (!repoName) return;
      fetch(`/api/repo/${repoName}/prs/${pr.id}/submitreview`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ review: draft })
      })
      .then(res => {
          if (res.ok) {
              alert("Review submitted!");
              // potentially trigger a refresh or update UI state
              if (onRefresh) onRefresh();
          } else {
              res.text().then(t => alert("Failed to submit: " + t));
          }
      })
      .catch(err => console.error("Failed to submit task draft", err));
  };
  
  const fetchTasks = () => {
    if (pr.type === 'pending' || pr.type === 'excluded') return;
    if (!repoName) return;

    fetch(`/api/repo/${repoName}/prs/${pr.id}/tasks`)
        .then(res => res.json())
        .then(data => {
            if (Array.isArray(data)) {
                setTasks(data);
            }
        })
        .catch(err => console.error("Failed to fetch tasks:", err));
  };



  useEffect(() => {
    fetchTasks();
    const interval = setInterval(fetchTasks, 10000);
    return () => clearInterval(interval);
  }, [pr.id, pr.type, pr.sandbox, lastUpdated, repoName]);

  const handleCreateTask = () => {
      if (!repoName) return;
      fetch(`/api/repo/${repoName}/prs/${pr.id}/tasks`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ 
              prompt: newTaskPrompt, 
              expectedComments: expectedComments,
              model: selectedModel
          })
      })
      .then(res => {
          if (res.ok) {
              setShowNewTaskForm(false);
              setNewTaskPrompt('');
              setExpectedComments(0);
              fetchTasks();
          } else {
              res.text().then(t => alert("Failed to create task: " + t));
          }
      })
      .catch(err => console.error("Failed to create task", err));
  };

  useEffect(() => {
    if (pr.type === 'pending' || pr.type === 'excluded') return;

    if (!isCollapsed && !diff && !diffError) {
      if (!pr.diffURL) {
        setDiffError("diffURL is empty");
        return;
      }
      fetch(`/api/proxy?url=${encodeURIComponent(pr.diffURL)}`)
        .then(async (res) => {
          if (res.ok) {
            return res.text();
          }
          const text = await res.text();
          throw new Error(`HTTP ${res.status}: ${res.statusText}. ${text}`);
        })
        .then(text => {
          if (text) {
            try {
              const files = parseDiff(text);
              setDiff(files);
              // Initialize fileCollapsed state here to ensure files are collapsed by default
              const initialCollapsedState = {};
              files.forEach(({ oldRevision, newRevision }) => {
                const fileId = oldRevision + '-' + newRevision;
                initialCollapsedState[fileId] = true;
              });
              setFileCollapsed(initialCollapsedState);
            } catch (e) {
              console.error("Failed to parse diff:", e);
              setDiffError(`Failed to parse diff: ${e.message}`);
            }
          } else {
            setDiff([]); // Empty diff
            setFileCollapsed({}); // Also reset collapsed state for empty diff
          }
        })
        .catch(err => {
          console.error("Failed to fetch diff:", err);
          setDiffError(err.message);
        });
    }
  }, [pr.diffURL, pr.id, isCollapsed, diff, diffError, pr.type]);

  if (pr.type === 'pending' || pr.type === 'excluded') {
    return (
      <div className="pr-card" style={{opacity: 0.6, border: '1px dashed #ccc'}}>
           <div className="pr-card-header" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '10px 20px'}}>
              <h3 style={{margin: 0}}>
                {pr.htmlURL ? (
                  <a href={pr.htmlURL} target="_blank" rel="noopener noreferrer" style={{color: 'inherit', textDecoration: 'none'}}>
                    {pr.title}
                  </a>
                ) : (
                  pr.title
                )}
              </h3>
              <button 
                className="btn" 
                onClick={(e) => { 
                    e.stopPropagation(); 
                    if (handleAddPR) handleAddPR(pr.id); 
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

  const isSubmitted = pr.reviewState === 'submitted';

  return (
    <div key={pr.id} className={`pr-card ${isSubmitted ? 'review-submitted' : ''}`}>
      <div className="pr-card-header" onClick={() => toggleCollapse(pr.id)} style={isMainView ? {cursor: 'default'} : {}}>
        <h3>
          <a href={pr.htmlURL} target="_blank" rel="noopener noreferrer">{pr.title} (PR #{pr.id})</a>
          {!isMainView && (
            <span style={{ marginLeft: '10px', fontSize: 'small', color: 'var(--text-secondary)' }}>
              {collapsedReviews[pr.id] ? 'click to expand' : 'click to collapse'}
            </span>
          )}
        </h3>
        <div className="pr-card-actions-header">
          {pr.labels && pr.labels.length > 0 && (
            <div style={{ display: 'flex', gap: '5px', marginRight: '10px' }}>
              {pr.labels.map((label, index) => (
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
          {getSandboxStatusClass(pr) === 'green' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              {pr.agentState === 'provisioning' ? (
                <span className="pr-sandbox" style={{backgroundColor: '#2196F3', color: 'white', cursor: 'default'}}>
                  Sandbox Provisioning
                </span>
              ) : (
                <a href={`/sandbox/${namespace}/${pr.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>
                  Sandbox Active
                </a>
              )}
              <button className="btn btn-sm pr-sandbox yellow" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleDown(pr.id); }} title="Scale Down">
                &#9646;&#9646;
              </button>
            </div>
          ) : getSandboxStatusClass(pr) === 'yellow' ? (
             <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
               <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox Paused</span>
               <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(pr.id, true); }} title="Scale Up">
                  &#9654;
               </button>
             </div>
          ) : getSandboxStatusClass(pr) === 'red' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`} title={pr.sandboxStatus || 'Error'}>
                {pr.sandboxStatus?.startsWith('Evicted') ? 'Evicted' : (pr.sandboxStatus || 'Error')}
              </span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(pr.id, true); }} title="Restart/Reprovision Sandbox">
                  &#8635;
               </button>
            </div>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox: Not created</span>
          )}
          <button className="btn btn-delete" style={{ fontSize: '14px', padding: '4px 10px' }} onClick={(e) => { e.stopPropagation(); handleDelete(pr.id); }}>&#x2715;</button>
        </div>
      </div>
      {!collapsedReviews[pr.id] && (
        <>
            {tasks.slice().reverse().map(task => (
                <TaskReviewCard 
                    key={task.name}
                    task={task}
                    prId={pr.id}
                    repoName={repoName}
                    drafts={drafts} // kept for backward compatibility if needed, but local draft is used
                    reviewViewModes={reviewViewModes}
                    yamlDrafts={yamlDrafts} // kept for backward compatibility
                    handleSaveDraft={handleSaveDraft} // kept for backward compatibility
                    handleDraftChange={handleDraftChange} // kept
                    handleRemoveComment={handleRemoveComment}
                    toggleReviewView={toggleReviewView}
                    handleYamlDraftChange={handleYamlDraftChange}
                    handleYamlDraftBlur={handleYamlDraftBlur}
                    handleSubmit={handleSubmit} // Original handler
                    handleSubmitTask={handleSubmitTask} // New handler
                    handleSaveTaskDraft={handleSaveTaskDraft} // New handler
                    handleExportCurl={handleExportCurl}
                    namespace={namespace}
                    isSubmitted={isSubmitted}
                    diff={diff}
                    diffError={diffError}
                    fileCollapsed={fileCollapsed}
                    setFileCollapsed={setFileCollapsed}
                    handleMoveCommentAndSave={handleMoveCommentAndSave}
                    lastDragTargetRef={lastDragTargetRef}
                    setCurlCommand={setCurlCommand}
                    curlCommand={curlCommand}
                    prUrl={pr.htmlURL}
                />
            ))}

            {!isSubmitted && (
                <div style={{padding: '10px', borderTop: '1px solid var(--border-color)', marginTop: '10px', display: 'flex', gap: '10px', flexDirection: 'column'}}>
                    <div style={{display: 'flex', gap: '10px'}}>
                        {!showNewTaskForm && (
                            <button className="btn" onClick={() => setShowNewTaskForm(true)}>Review Again</button>
                        )}
                    </div>

                    {showNewTaskForm && (
                        <div className="new-task-form" style={{padding: '10px', backgroundColor: 'var(--bg-secondary)', borderRadius: '5px'}}>
                            <h4>Request New Review Task</h4>
                            <div style={{marginBottom: '10px'}}>
                                <label style={{marginRight: '10px', display: 'block', marginBottom: '5px'}}>
                                    Expected Comments: {expectedComments === 0 ? 'Auto' : expectedComments}
                                </label>
                                <input 
                                    type="range" 
                                    min="0" 
                                    max="50" 
                                    value={expectedComments} 
                                    onChange={(e) => setExpectedComments(parseInt(e.target.value))}
                                    style={{width: '100%'}}
                                />
                                <div style={{display: 'flex', justifyContent: 'space-between', fontSize: 'small', color: 'var(--text-secondary)'}}>
                                    <span>Auto</span>
                                    <span>50</span>
                                </div>
                            </div>
                            <div style={{marginBottom: '10px'}}>
                                <label style={{fontSize: 'small', color: 'var(--text-secondary)', display: 'block', marginBottom: '5px'}}>Model:</label>
                                <select 
                                    value={selectedModel} 
                                    onChange={(e) => setSelectedModel(e.target.value)}
                                    style={{width: '100%', padding: '8px', borderRadius: '4px', border: '1px solid var(--border-color)', backgroundColor: 'var(--bg-primary)', color: 'var(--text-primary)'}}
                                >
                                    {reviewModels.map(m => <option key={m} value={m}>{m}</option>)}
                                </select>
                            </div>
                            <textarea 
                                className="review-textarea"
                                value={newTaskPrompt}
                                onChange={(e) => setNewTaskPrompt(e.target.value)}
                                placeholder="Enter custom instructions for the agent (optional)..."
                                style={{width: '100%', marginBottom: '10px'}}
                            />
                            <div>
                                <button className="btn btn-submit" onClick={handleCreateTask}>Start Task</button>
                                <button className="btn" style={{marginLeft: '10px', backgroundColor: 'var(--status-grey)'}} onClick={() => setShowNewTaskForm(false)}>Cancel</button>
                            </div>
                        </div>
                    )}


                </div>
            )}
            
            {/* Show CURL command if set - GLOBAL to PR since it's an action outcome */}
            {curlCommand && (
                <div className="curl-command-display" style={{marginTop: '10px'}}>
                <h4>Curl Command</h4>
                <textarea
                    className="review-textarea"
                    style={{height: '150px', fontFamily: 'monospace', width: '100%'}}
                    value={curlCommand}
                    readOnly
                />
                <button className="btn" style={{marginTop: '5px'}} onClick={() => {
                    navigator.clipboard.writeText(curlCommand);
                    alert("Copied to clipboard!");
                }}>Copy to Clipboard</button>
                <button className="btn" style={{marginTop: '5px', marginLeft: '10px'}} onClick={() => setCurlCommand(null)}>Close</button>
                </div>
            )}
        </>
      )}
    </div>
  );
}

export default PrReviewCard;