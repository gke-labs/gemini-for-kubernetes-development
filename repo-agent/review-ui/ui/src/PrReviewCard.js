import React, { useState, useEffect, useRef } from 'react';
import yaml from 'js-yaml';
import { parseDiff, Diff, getChangeKey } from 'react-diff-view';
import 'react-diff-view/style/index.css';


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
}) {
  const [diff, setDiff] = useState(null);
  const [diffError, setDiffError] = useState(null);
  const [fileCollapsed, setFileCollapsed] = useState({});
  const [reviewFlairText, setReviewFlairText] = useState('');
  const [curlCommand, setCurlCommand] = useState(null);
  const lastDragTargetRef = useRef(null);

  const getReviewFlairColor = (flairText) => {
    if (!flairText) return '#3e7f67ff';
    const text = flairText.toLowerCase();
    if (text === 'done' || text === 'review ready') return 'green';
    if (text.includes('reviewing')) return 'orange';
    if (text.includes('error')) return '#9e2a2aff';
    if (text === 'submitted' || text === 'review draft created') return '#3f5398ff';
    return '#cd9945ff'; // Default color
  };

  const isCollapsed = collapsedReviews[pr.id];
  useEffect(() => {
    if (pr.type === 'pending' || pr.type === 'excluded') return;

    if (pr.reviewState === 'submitted') {
      setReviewFlairText('Review Draft Created');
    } else if (pr.agentState) {
      setReviewFlairText(pr.agentState);
    } else if (drafts[pr.id] && drafts[pr.id].note && drafts[pr.id].note.trim() !== '') {
      setReviewFlairText('Ready');
    } else {
      setReviewFlairText('Generating ...');
    }
  }, [drafts, pr.id, pr.type, pr.agentState, pr.reviewState]);

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

  const renderDiffView = () => {
    if (diffError) {
      return <div className="diff-container error">Could not load diff: {diffError}</div>;
    }
    if (!diff) {
      return <div className="diff-container">Loading diff...</div>;
    }

    const comments = drafts[pr.id]?.review?.comments || [];
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
                      e.dataTransfer.setData('application/json', JSON.stringify({ prId: pr.id, commentIndex: comment.index }));
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
                          onChange={(e) => handleDraftChange(pr.id, 'comment.body', e.target.value, comment.index)}
                          onBlur={() => handleSaveDraft(pr.id)}
                          placeholder="Line-specific comment..."
                        ></textarea>
                        <button className="btn btn-remove-comment" onClick={() => handleRemoveComment(pr.id, comment.index)}>Remove</button>
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
            console.log('Drop event triggered');

            if (lastDragTargetRef.current) {
              lastDragTargetRef.current.style.backgroundColor = '';
              lastDragTargetRef.current = null;
            }

            const commentDataText = e.dataTransfer.getData('application/json');
            if (!commentDataText) {
                console.error('No comment data in dataTransfer.');
                return;
            }
            console.log('Comment data text:', commentDataText);
            const commentData = JSON.parse(commentDataText);
            const { prId, commentIndex } = commentData;
            console.log('Parsed comment data:', { prId, commentIndex });


            let target = e.target;
            while (target && !target.classList.contains('diff-line')) {
                target = target.parentElement;
            }

            if (!target) {
                console.error('Could not find diff-line target.');
                return;
            }
            console.log('Drop target:', target);

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
            console.log('Calculated side:', side);

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
            console.log('Calculated line:', line);

            if (line && side) {
                console.log('Calling handleMoveCommentAndSave with:', { prId, commentIndex, path, line, side });
                handleMoveCommentAndSave(prId, commentIndex, path, line, side);
            } else {
                console.error('Could not determine line and/or side for drop.');
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
                <span style={{ marginLeft: '10px', fontSize: 'small', color: '#555' }}>
                  {isFileCollapsed ? 'click to expand' : 'click to collapse'}
                </span>
              </div>
              {!isFileCollapsed && (
                <div onDragOver={handleDragOverFile} onDrop={handleDrop} onDragLeave={handleDragLeaveFile}>
                  {unplacedComments.length > 0 && (
                    <div className="diff-widget" style={{padding: '10px', borderBottom: '1px solid #ddd'}}>
                      <h6>Comments on lines not shown in diff or file-level comments</h6>
                      {unplacedComments.map(comment => (
                        <div
                          key={comment.index}
                          style={{ borderTop: '1px solid #eee', paddingTop: '5px', marginTop: '5px', cursor: isSubmitted ? 'default' : 'move' }}
                          draggable={!isSubmitted}
                          onDragStart={e => {
                              if (isSubmitted) return;
                              e.dataTransfer.setData('application/json', JSON.stringify({ prId: pr.id, commentIndex: comment.index }));
                              e.stopPropagation();
                          }}
                        >
                          {isSubmitted ? (
                            <>
                              {comment.line && <p style={{fontSize: 'small', color: '#555', marginBottom: '5px'}}>Line: {comment.line} ({comment.side || 'RIGHT'})</p>}
                              <pre className="review-pre">{comment.body}</pre>
                            </>
                          ) : (
                            <>
                              {comment.line && <p style={{fontSize: 'small', color: '#555', marginBottom: '5px'}}>Line: {comment.line} ({comment.side || 'RIGHT'})</p>}
                              <textarea
                                className="review-textarea"
                                value={comment.body || ''}
                                onChange={(e) => handleDraftChange(pr.id, 'comment.body', e.target.value, comment.index)}
                                onBlur={() => handleSaveDraft(pr.id)}
                                placeholder="Comment..."
                              ></textarea>
                              <button className="btn btn-remove-comment" onClick={() => handleRemoveComment(pr.id, comment.index)}>Remove</button>
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
    <div key={pr.id} className={`pr-card ${isSubmitted ? 'review-submitted' : ''}`}>
      <div className="pr-card-header" onClick={() => toggleCollapse(pr.id)} style={isMainView ? {cursor: 'default'} : {}}>
        <h3>
          <a href={pr.htmlURL} target="_blank" rel="noopener noreferrer">{pr.title} (PR #{pr.id})</a>
          {!isMainView && (
            <span style={{ marginLeft: '10px', fontSize: 'small', color: '#555' }}>
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
          {reviewFlairText && pr.agentState !== 'provisioning' && (
            <span 
              style={{ marginRight: '10px', backgroundColor: getReviewFlairColor(reviewFlairText), color: 'white', padding: '5px 10px', borderRadius: '5px', fontSize: 'small' }}
              title={pr.agentStateMessage || ''}
            >
              {reviewFlairText}
            </span>
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
               <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(pr.id); }} title="Scale Up">
                  &#9654;
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
          <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '10px 0' }}>
            <button className="btn" onClick={() => toggleReviewView(pr.id)}>
              {reviewViewModes[pr.id] === 'structured' ? 'View as YAML' : 'View as Structured'}
            </button>
          </div>
          {isSubmitted ? (
            reviewViewModes[pr.id] === 'structured' ? (
              <div className="review-display">
                <strong>Review:</strong>
                {drafts[pr.id]?.note &&
                  <div className="review-section">
                    <h4>Note to Reviewer</h4>
                    <pre className="review-pre">{drafts[pr.id].note}</pre>
                  </div>
                }
                {drafts[pr.id]?.review?.body &&
                  <div className="review-section">
                    <h4>GitHub Review</h4>
                    <pre className="review-pre">{drafts[pr.id].review.body}</pre>
                  </div>
                }
              </div>
            ) : (
              <div className="review-display">
                <strong>Review:</strong>
                <pre>{yamlDrafts[pr.id] || ''}</pre>
              </div>
            )
          ) : (
            reviewViewModes[pr.id] === 'structured' ? (
              <div className="review-form">
                <div className="review-section">
                  <h4>Note to Reviewer</h4>
                  <textarea
                    className="review-textarea"
                    value={drafts[pr.id]?.note || ''}
                    onChange={(e) => handleDraftChange(pr.id, 'note', e.target.value)}
                    onBlur={() => handleSaveDraft(pr.id)}
                    placeholder="A description of the changes as a note to the reviewer..."
                  ></textarea>
                </div>
                <div className="review-section">
                  <h4>GitHub Review</h4>
                  <textarea
                    className="review-textarea"
                    value={drafts[pr.id]?.review?.body || ''}
                    onChange={(e) => handleDraftChange(pr.id, 'review.body', e.target.value)}
                    onBlur={() => handleSaveDraft(pr.id)}
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
                    value={yamlDrafts[pr.id] || ''}
                    onChange={(e) => handleYamlDraftChange(pr.id, e.target.value)}
                    onBlur={() => handleYamlDraftBlur(pr.id)}
                    placeholder="Enter review as YAML..."
                  ></textarea>
                </div>
              </div>
            )
          )}
          {renderDiffView()}
          <div className="pr-card-actions">
            {!isSubmitted && (
              <button className="btn btn-submit" onClick={() => handleSubmit(pr.id)}>
                Create Draft Review
              </button>
            )}
            {isSubmitted && (
              <a href={pr.htmlURL} target="_blank" rel="noopener noreferrer" className="btn btn-submit" style={{textDecoration: 'none'}}>
                Go to review
              </a>
            )}
            <button className="btn btn-submit" style={{marginLeft: '10px', backgroundColor: '#6c757d'}} onClick={() => handleExportCurl(pr.id, setCurlCommand)} disabled={isSubmitted}>
              Export Curl Command
            </button>
          </div>
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
