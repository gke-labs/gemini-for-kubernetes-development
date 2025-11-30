import React, { useState, useEffect } from 'react';

function IssueCard({
  issue,
  drafts,
  activeSubTab,
  handleIssueDraftChange,
  handleIssueSaveDraft,
  handleIssueSubmit,
  handleIssueDelete,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
}) {
  const [isCollapsed, setIsCollapsed] = useState(true);
  const [reviewFlairText, setReviewFlairText] = useState('');

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

  const toggleCollapse = () => {
    setIsCollapsed(!isCollapsed);
  };

  useEffect(() => {
    if (issue.comment) {
      setReviewFlairText('Submitted');
    } else if (drafts[issue.id] && drafts[issue.id].trim() !== '') {
      setReviewFlairText('Ready');
    } else {
      setReviewFlairText('Generating ...');
    }
  }, [issue.comment, drafts, issue.id]);

  return (
    <div key={issue.id} className={`pr-card ${issue.comment ? 'review-submitted' : ''}`}>
      <div className="pr-card-header" onClick={toggleCollapse}>
        <h3>
          <a href={issue.htmlURL} target="_blank" rel="noopener noreferrer">{issue.title} (Issue #{issue.id})</a>
          <span style={{ marginLeft: '10px', fontSize: 'small', color: '#555' }}>
            {isCollapsed ? 'click to expand' : 'click to collapse'}
          </span>
        </h3>
        <div className="pr-card-actions-header">
          {reviewFlairText && (
            <span style={{ marginRight: '10px', backgroundColor: getReviewFlairColor(reviewFlairText), color: 'white', padding: '5px 10px', borderRadius: '5px', fontSize: 'small' }}>
              {reviewFlairText}
            </span>
          )}
          {getSandboxStatusClass(issue) === 'green' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <a href={`/sandbox/${namespace}/${issue.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>
                Sandbox Active
              </a>
               <button className="btn btn-sm pr-sandbox yellow" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleDown(issue.id, activeSubTab.name); }} title="Scale Down">
                &#9646;&#9646;
              </button>
            </div>
          ) : getSandboxStatusClass(issue) === 'yellow' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox Paused</span>
              <button className="btn btn-sm pr-sandbox green" style={{padding: '4px 10px', fontSize: '14px'}} onClick={(e) => { e.stopPropagation(); handleScaleUp(issue.id, activeSubTab.name); }} title="Scale Up">
                  &#9654;
               </button>
            </div>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox: Not created</span>
          )}
        </div>
      </div>
      {!isCollapsed && (
        <>
          {issue.pushBranch ? (
            <div className="branch-link">
              <strong>Branch: </strong> <a href={issue.branchURL} target="_blank" rel="noopener noreferrer">{issue.branchURL}</a>
            </div>
          ) : issue.comment ? (
            <div className="review-display">
              <strong>Comment:</strong>
              <p>{issue.comment}</p>
            </div>
          ) : (
            <textarea
              className="review-textarea"
              value={drafts[issue.id] || ''}
              onChange={(e) => handleIssueDraftChange(issue.id, e.target.value)}
              onBlur={() => handleIssueSaveDraft(issue.id, activeSubTab.name)}
              placeholder="Leave a comment..."
            ></textarea>
          )}
          <div className="pr-card-actions">
            {!issue.pushBranch && (
              <button className="btn btn-submit" onClick={() => handleIssueSubmit(issue.id, activeSubTab.name)} disabled={!!issue.comment}>
                {issue.comment ? 'Submitted' : 'Create Comment'}
              </button>
            )}
            <button className="btn btn-delete" onClick={() => handleIssueDelete(issue.id, activeSubTab.name)}>&#x2715;</button>
          </div>
        </>
      )}
    </div>
  );
}

export default IssueCard;
