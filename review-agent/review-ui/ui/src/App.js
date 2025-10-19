import React, { useState, useEffect } from 'react';
import './App.css';

function App() {
  const [repos, setRepos] = useState([]);
  const [activeRepo, setActiveRepo] = useState('');
  const [prs, setPrs] = useState([]);
  const [drafts, setDrafts] = useState({});

  useEffect(() => {
    fetch('/api/repos')
      .then(res => res.json())
      .then(data => {
        setRepos(data);
        if (data.length > 0) {
          setActiveRepo(data[0].name);
        }
      })
      .catch(err => console.error("Failed to fetch repos:", err));
  }, []);

  useEffect(() => {
    if (activeRepo) {
      fetch(`/api/repo/${activeRepo}/prs`)
        .then(res => res.json())
        .then(data => {
          setPrs(data);
          const initialDrafts = {};
          data.forEach(pr => {
            initialDrafts[pr.id] = pr.draft || '';
          });
          setDrafts(initialDrafts);
        })
        .catch(err => console.error(`Failed to fetch PRs for ${activeRepo}:`, err));
    }
  }, [activeRepo]);

  const handleDelete = (id) => {
    fetch(`/api/repo/${activeRepo}/prs/${id}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) {
          setPrs(prs.filter(pr => pr.id !== id));
        } else {
          alert("Failed to delete PR sandbox");
        }
      })
      .catch(err => console.error("Failed to delete PR:", err));
  };

  const handleSaveDraft = (id) => {
    const draft = drafts[id];
    fetch(`/api/repo/${activeRepo}/prs/${id}/draft`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft })
    }).catch(err => console.error("Failed to save draft:", err));
  };

  const handleDraftChange = (id, value) => {
    setDrafts(prevDrafts => ({
      ...prevDrafts,
      [id]: value
    }));
  };

  const handleSubmit = (id) => {
    const review = drafts[id];
    if (!review.trim()) {
      alert("Please leave a review comment before Submitting.");
      return;
    }
    fetch(`/api/repo/${activeRepo}/prs/${id}/submitreview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ review })
    })
    .then(res => {
      if (res.ok) {
        setPrs(prs.map(pr => pr.id === id ? { ...pr, review, draft: '' } : pr));
      } else {
        alert("Failed to submit PR review");
      }
    })
    .catch(err => console.error("Failed to submit PR review:", err));
  };

  const getSandboxStatusClass = (pr) => {
    if (!pr.sandbox) {
      return 'grey';
    }
    if (pr.sandboxReplica === "0") {
      return 'yellow';
    }
    return 'green';
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>Pull Request Reviews</h1>
      </header>
      <nav className="repo-tabs">
        {repos.map(repo => (
          <button
            key={repo.name}
            className={`tab-btn ${activeRepo === repo.name ? 'active' : ''}`}
            onClick={() => setActiveRepo(repo.name)}
          >
            {repo.name}
          </button>
        ))}
      </nav>
      <main className="pr-list">
        {prs.map(pr => (
          <div key={pr.id} className={`pr-card ${pr.review ? 'review-submitted' : ''}`}>
            <div className="pr-card-header">
              <h3><a href={pr.htmlURL} target="_blank" rel="noopener noreferrer">{pr.title} (PR #{pr.id})</a></h3>
              {getSandboxStatusClass(pr) === 'green' ? (
                <a href={`/sandbox/${pr.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>
                  Sandbox: {pr.sandbox}
                </a>
              ) : (
                <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox: {pr.sandbox || 'Not created'}</span>
              )}
            </div>
            {pr.review ? (
              <div className="review-display">
                <strong>Review:</strong>
                <p>{pr.review}</p>
              </div>
            ) : (
              <textarea
                className="review-textarea"
                value={drafts[pr.id] || ''}
                onChange={(e) => handleDraftChange(pr.id, e.target.value)}
                onBlur={() => handleSaveDraft(pr.id)}
                placeholder="Leave a review comment..."
              ></textarea>
            )}
            <div className="pr-card-actions">
              <button className="btn btn-submit" onClick={() => handleSubmit(pr.id)} disabled={!!pr.review}>
                {pr.review ? 'Submitted' : 'Submit'}
              </button>
              <button className="btn btn-delete" onClick={() => handleDelete(pr.id)}>Delete</button>
            </div>
          </div>
        ))}
      </main>
    </div>
  );
}

export default App;