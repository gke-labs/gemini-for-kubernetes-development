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
  handleMoveCommentAndSave,
}) {
  const [diff, setDiff] = useState(null);
  const [diffError, setDiffError] = useState(null);
  const [fileCollapsed, setFileCollapsed] = useState({});
  const [reviewFlairText, setReviewFlairText] = useState('');
  const [curlCommand, setCurlCommand] = useState(null);
  const lastDragTargetRef = useRef(null);

  const getReviewFlairColor = (flairText) => {
    switch (flairText) {
      case 'Ready':
        return 'green';
      case 'Generating ...':
        return 'orange';
      case 'Submitted':
        return '#3f5398ff';
      default:
        return '#3e7f67ff'; // Default color
    }
  };

  const isCollapsed = collapsedReviews[pr.id];
  useEffect(() => {
    if (pr.review) {
      setReviewFlairText('Submitted');
    } else if (drafts[pr.id] && drafts[pr.id].note && drafts[pr.id].note.trim() !== '') {
      setReviewFlairText('Ready');
    } else {
      setReviewFlairText('Generating ...');
    }
  }, [pr.review, drafts, pr.id]);
  useEffect(() => {
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
  }, [pr.diffURL, pr.id, isCollapsed, diff, diffError]);

  const reviewData = pr.review ? yaml.load(pr.review) : null;

  const renderDiffView = () => {
    if (diffError) {
      return <div className="diff-container error">Could not load diff: {diffError}</div>;
    }
    if (!diff) {
      return <div className="diff-container">Loading diff...</div>;
    }

    const comments = pr.review ? (reviewData?.review?.comments || []) : (drafts[pr.id]?.review?.comments || []);
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
                    draggable={!pr.review}
                    onDragStart={e => {
                      if (pr.review) return;
                      e.dataTransfer.setData('application/json', JSON.stringify({ prId: pr.id, commentIndex: comment.index }));
                      e.stopPropagation();
                    }}
                    style={{ cursor: pr.review ? 'default' : 'move' }}
                  >
                    {pr.review ? (
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
            const oldLineGutter = gutters[0];
            const newLineGutter = gutters[1];

            const rect = target.getBoundingClientRect();
            const isRightSide = e.clientX > rect.left + rect.width / 2;
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
                          style={{ borderTop: '1px solid #eee', paddingTop: '5px', marginTop: '5px', cursor: pr.review ? 'default' : 'move' }}
                          draggable={!pr.review}
                          onDragStart={e => {
                              if (pr.review) return;
                              e.dataTransfer.setData('application/json', JSON.stringify({ prId: pr.id, commentIndex: comment.index }));
                              e.stopPropagation();
                          }}
                        >
                          {pr.review ? (
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
    <div key={pr.id} className={`pr-card ${pr.review ? 'review-submitted' : ''}`}>
      <div className="pr-card-header" onClick={() => toggleCollapse(pr.id)}>
        <h3>
          <a href={pr.htmlURL} target="_blank" rel="noopener noreferrer">{pr.title} (PR #{pr.id})</a>
          <span style={{ marginLeft: '10px', fontSize: 'small', color: '#555' }}>
            {collapsedReviews[pr.id] ? 'click to expand' : 'click to collapse'}
          </span>
        </h3>
        <div className="pr-card-actions-header">
          {reviewFlairText && (
            <span style={{ marginRight: '10px', backgroundColor: getReviewFlairColor(reviewFlairText), color: 'white', padding: '5px 10px', borderRadius: '5px', fontSize: 'small' }}>
              {reviewFlairText}
            </span>
          )}
          {getSandboxStatusClass(pr) === 'green' ? (
            <a href={`/sandbox/${pr.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>
              Sandbox &#9654;
            </a>
          ) : getSandboxStatusClass(pr) === 'yellow' ? (
            <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox &#9646;&#9646;</span>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox: Not created</span>
          )}
        </div>
      </div>
      {!collapsedReviews[pr.id] && (
        <>
          <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '10px 0' }}>
            <button className="btn" onClick={() => toggleReviewView(pr.id)}>
              {reviewViewModes[pr.id] === 'structured' ? 'View as YAML' : 'View as Structured'}
            </button>
          </div>
          {pr.review ? (
            reviewViewModes[pr.id] === 'structured' ? (
              <div className="review-display">
                <strong>Review:</strong>
                {reviewData.note &&
                  <div className="review-section">
                    <h4>Note to Reviewer</h4>
                    <pre className="review-pre">{reviewData.note}</pre>
                  </div>
                }
                {reviewData.review && reviewData.review.body &&
                  <div className="review-section">
                    <h4>GitHub Review</h4>
                    <pre className="review-pre">{reviewData.review.body}</pre>
                  </div>
                }
              </div>
            ) : (
              <div className="review-display">
                <strong>Review:</strong>
                <pre>{pr.review}</pre>
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
            <button className="btn btn-submit" onClick={() => handleSubmit(pr.id)} disabled={!!pr.review}>
              {pr.review ? 'Draft Created' : 'Create Draft Review'}
            </button>
            <button className="btn btn-submit" style={{marginLeft: '10px', backgroundColor: '#6c757d'}} onClick={() => handleExportCurl(pr.id, setCurlCommand)} disabled={!!pr.review}>
              Export Curl Command
            </button>
          <button className="btn btn-delete" onClick={(e) => { e.stopPropagation(); handleDelete(pr.id); }}>&#x2715;</button>
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