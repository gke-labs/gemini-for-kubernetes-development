import React from 'react';
import yaml from 'js-yaml';

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
  toggleCollapse,
  getSandboxStatusClass,
}) {
  const reviewData = pr.review ? yaml.load(pr.review) : null;

  return (
    <div key={pr.id} className={`pr-card ${pr.review ? 'review-submitted' : ''}`}>
      <div className="pr-card-header">
        <h3><a href={pr.htmlURL} target="_blank" rel="noopener noreferrer">{pr.title} (PR #{pr.id})</a></h3>
        <div className="pr-card-actions-header">
          {getSandboxStatusClass(pr) === 'green' ? (
            <a href={`/sandbox/${pr.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>
              Sandbox: {pr.sandbox}
            </a>
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox: {pr.sandbox || 'Not created'}</span>
          )}
          <button className="btn" onClick={() => toggleReviewView(pr.id)}>
            {reviewViewModes[pr.id] === 'structured' ? 'YAML' : 'Structured'}
          </button>
          <button className="btn btn-toggle-collapse" onClick={() => toggleCollapse(pr.id)}>
            {collapsedReviews[pr.id] ? 'Expand' : 'Collapse'}
          </button>
        </div>
      </div>
      {!collapsedReviews[pr.id] && (
        pr.review ? (
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
              {reviewData.review && reviewData.review.comments && reviewData.review.comments.length > 0 &&
                <div className="review-section">
                  <h5>File Comments</h5>
                  {reviewData.review.comments.map((comment, index) => (
                    <div key={index} className="comment-card">
                      <div className="comment-card-header">
                        <span>{comment.path}:{comment.line}</span>
                      </div>
                      <pre className="review-pre">{comment.body}</pre>
                    </div>
                  ))}
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
              <div className="review-section">
                <h5>File Comments</h5>
                {drafts[pr.id]?.review?.comments?.map((comment, index) => (
                  <div key={index} className="comment-card">
                    <div className="comment-card-header">
                      <span>{comment.path}:{comment.line}</span>
                      <button className="btn btn-remove-comment" onClick={() => handleRemoveComment(pr.id, index)}>Remove</button>
                    </div>
                    <textarea
                      className="review-textarea"
                      value={comment.body || ''}
                      onChange={(e) => handleDraftChange(pr.id, 'comment.body', e.target.value, index)}
                      onBlur={() => handleSaveDraft(pr.id)}
                      placeholder="Line-specific comment..."
                    ></textarea>
                  </div>
                ))}
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
        )
      )}
      {!collapsedReviews[pr.id] && (
        <div className="pr-card-actions">
          <button className="btn btn-submit" onClick={() => handleSubmit(pr.id)} disabled={!!pr.review}>
            {pr.review ? 'Submitted' : 'Submit'}
          </button>
          <button className="btn btn-delete" onClick={() => handleDelete(pr.id)}>Delete</button>
        </div>
      )}
    </div>
  );
}

export default PrReviewCard;
