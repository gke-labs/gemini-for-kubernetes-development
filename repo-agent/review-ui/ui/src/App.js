import React, { useState, useEffect, useCallback, useRef } from 'react';
import yaml from 'js-yaml';
import './App.css';
import PrReviewCard from './PrReviewCard';
import Review from './Review';
import Issues from './Issues';
import IssueCard from './IssueCard';
import DevCard from './DevCard';
import AddRepo from './AddRepo';
import DeleteRepo from './DeleteRepo';
import Settings from './Settings';
import UpdateRepo from './UpdateRepo';

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isGuest, setIsGuest] = useState(false);
  const [isLoadingAuth, setIsLoadingAuth] = useState(true);
  const [user, setUser] = useState(null);
  const [view, setView] = useState('dashboard'); // 'dashboard', 'settings', 'add_repo'
  const [githubAuthEnabled, setGithubAuthEnabled] = useState(false);
  const [showGithubConfig, setShowGithubConfig] = useState(false);
  const [githubClientId, setGithubClientId] = useState('');
  const [githubClientSecret, setGithubClientSecret] = useState('');
  const [isGeminiKeySet, setIsGeminiKeySet] = useState(true); // Default to true to avoid flash of warning
  const [configError, setConfigError] = useState('');

  const [repos, setRepos] = useState([]);
  const [activeRepo, setActiveRepo] = useState(null);
  const activeRepoRef = useRef(activeRepo);
  const hasRedirectedMissingKey = useRef(false);
  useEffect(() => { activeRepoRef.current = activeRepo; }, [activeRepo]);

  const [activeSubTab, setActiveSubTab] = useState({ repo: '', name: '' });
  const [prs, setPrs] = useState([]);
  const [issues, setIssues] = useState([]);
  const [devSandboxes, setDevSandboxes] = useState([]);
  const [drafts, setDrafts] = useState({});
  const [collapsedReviews, setCollapsedReviews] = useState({});
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'light');
  const [reviewViewModes, setReviewViewModes] = useState({});
  const [yamlDrafts, setYamlDrafts] = useState({});
  const [lastUpdated, setLastUpdated] = useState(null);
  const [hasInstructionDraft, setHasInstructionDraft] = useState(false);
  const [devModalOpen, setDevModalOpen] = useState(false);
  const [newDevBranch, setNewDevBranch] = useState('');
  const [newDevPrompt, setNewDevPrompt] = useState('');
  const [feedbackModalOpen, setFeedbackModalOpen] = useState(false);
  const [feedbackTitle, setFeedbackTitle] = useState('');
  const [feedbackText, setFeedbackText] = useState('');
  const [feedbackImage, setFeedbackImage] = useState('');
  const [isSubmittingFeedback, setIsSubmittingFeedback] = useState(false);


  useEffect(() => {
    document.body.className = theme === 'dark' ? 'dark-mode' : '';
    localStorage.setItem('theme', theme);
  }, [theme]);

  // Check authentication status on load
  useEffect(() => {
    fetch('/api/auth/status')
      .then(res => {
        if (res.ok) return res.json();
        throw new Error("Not authenticated");
      })
      .then(data => {
        setIsAuthenticated(true);
        setUser(data.user);
        setIsLoadingAuth(false);
      })
      .catch(() => {
        setIsAuthenticated(false);
        setIsLoadingAuth(false);
      });
      
    fetch('/api/auth/providers')
      .then(res => res.json())
      .then(data => {
        setGithubAuthEnabled(data.github);
      })
      .catch(err => console.error("Failed to fetch auth providers:", err));
  }, []);

  useEffect(() => {
    if (isAuthenticated || isGuest) {
      fetch('/api/settings')
        .then(res => res.json())
        .then(data => {
          setIsGeminiKeySet(data.gemini_api_key_set);
          if (!data.gemini_api_key_set && !hasRedirectedMissingKey.current && isAuthenticated) {
            hasRedirectedMissingKey.current = true;
            setView('settings');
          }
        })
        .catch(err => console.error("Failed to fetch settings:", err));
    }
  }, [isAuthenticated, isGuest]);

  useEffect(() => {
    if (activeRepo && (isAuthenticated || isGuest)) {
        fetch(`/api/repos/${activeRepo.name}/instructions`)
            .then(res => {
                if (res.ok) return res.json();
                throw new Error("Failed to fetch instructions");
            })
            .then(data => {
                setHasInstructionDraft(!!data.draft);
            })
            .catch(err => {
                console.error("Failed to check instructions draft:", err);
                setHasInstructionDraft(false);
            });
    } else {
        setHasInstructionDraft(false);
    }
  }, [activeRepo, isAuthenticated, isGuest]);

  const handleGithubConfigSubmit = (e) => {
    e.preventDefault();
    setConfigError('');
    fetch('/api/auth/github-config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ client_id: githubClientId, client_secret: githubClientSecret })
    })
    .then(async (res) => {
      if (res.ok) {
        setGithubAuthEnabled(true);
        setShowGithubConfig(false);
      } else {
        const data = await res.json();
        throw new Error(data.error || 'Failed to update config');
      }
    })
    .catch(err => setConfigError(err.message));
  };

  const fetchRepos = useCallback(() => {
    if (!isAuthenticated && !isGuest) return;
    fetch('/api/repos')
      .then(res => res.json())
      .then(data => {
        const safeData = data || [];
        setRepos(safeData);
        
        const currentActiveRepo = activeRepoRef.current;
        if (currentActiveRepo) {
           const updatedRepo = safeData.find(r => r.name === currentActiveRepo.name);
           if (updatedRepo) {
               setActiveRepo(updatedRepo);
           } else {
               setActiveRepo(null);
           }
        } else if (safeData.length > 0 && view === 'dashboard') {
           handleRepoClick(safeData[0].name, safeData);
        }
      })
      .catch(err => console.error("Failed to fetch repos:", err));
  }, [isAuthenticated, isGuest, view]);

  useEffect(() => {
    if (isAuthenticated || isGuest) {
        fetchRepos();
    }
  }, [isAuthenticated, isGuest, fetchRepos]);

  const refreshData = useCallback((merge = false) => {
    if (!isAuthenticated && !isGuest) return;
    if (!activeRepo) return;
    if (activeSubTab.repo !== activeRepo.name) return;

    if (activeSubTab.name === 'review') {
        if (!merge) setIssues([]);
        fetch(`/api/repo/${activeRepo.name}/prs`)
          .then(res => res.json())
          .then(data => {
            const safeData = data || [];
            setPrs(safeData);
            setLastUpdated(new Date());
            
            setDrafts(prev => {
                const next = merge ? { ...prev } : {};
                safeData.forEach(pr => {
                  let parsedDraft = null;
                  try {
                    parsedDraft = yaml.load(pr.draft || '');
                  } catch (e) {
                    console.error(`Error parsing draft YAML for PR ${pr.id}:`, e);
                  }
                  const serverDraftObj = parsedDraft || { note: '', review: { body: '', comments: [] } };

                  if (next[pr.id] === undefined) {
                    next[pr.id] = serverDraftObj;
                  } else {
                    const local = next[pr.id];
                    const isLocalEmpty = !local.note?.trim() && !local.review?.body?.trim() && (!local.review?.comments || local.review.comments.length === 0);
                    const isServerEmpty = !serverDraftObj.note?.trim() && !serverDraftObj.review?.body?.trim() && (!serverDraftObj.review?.comments || serverDraftObj.review.comments.length === 0);

                    if (isLocalEmpty && !isServerEmpty) {
                        next[pr.id] = serverDraftObj;
                    }
                  }
                });
                return next;
            });

            setCollapsedReviews(prev => {
                const next = merge ? { ...prev } : {};
                safeData.forEach(pr => {
                    if (next[pr.id] === undefined) next[pr.id] = true;
                });
                return next;
            });

            setReviewViewModes(prev => {
                const next = merge ? { ...prev } : {};
                safeData.forEach(pr => {
                     if (next[pr.id] === undefined) next[pr.id] = 'structured';
                });
                return next;
            });
          })
          .catch(err => console.error(`Failed to fetch PRs for ${activeRepo.name}:`, err));
      } else if (activeSubTab.name === 'dev') {
        if (!merge) setDevSandboxes([]);
        fetch(`/api/repo/${activeRepo.name}/dev`)
          .then(res => res.json())
          .then(data => {
            setDevSandboxes(data || []);
            setLastUpdated(new Date());
          })
          .catch(err => console.error(`Failed to fetch dev sandboxes for ${activeRepo.name}:`, err));
    } else if (activeSubTab.name) {
        if (!merge) setPrs([]);
        let url = `/api/repo/${activeRepo.name}/issues/${activeSubTab.name}`;
        if (activeSubTab.name === 'issues') {
             url = `/api/repo/${activeRepo.name}/issues`;
        }
        fetch(url)
          .then(res => res.json())
          .then(data => {
            const safeData = data || [];
            setIssues(safeData);
            setLastUpdated(new Date());
            setDrafts(prev => {
                const next = merge ? { ...prev } : {};
                safeData.forEach(issue => {
                    const serverDraft = issue.draft || '';
                    const isServerEmpty = !serverDraft.trim();

                    if (next[issue.id] === undefined) {
                        next[issue.id] = serverDraft;
                    } else {
                        const localDraft = next[issue.id];
                        const isLocalEmpty = !localDraft.trim();

                        if (isLocalEmpty && !isServerEmpty) {
                            next[issue.id] = serverDraft;
                        }
                    }
                });
                return next;
            });
          })
          .catch(err => console.error(`Failed to fetch issues for ${activeRepo.name} tab ${activeSubTab.name}:`, err));
    }
  }, [activeRepo, activeSubTab, isAuthenticated, isGuest]);

  useEffect(() => {
    if ((!isAuthenticated && !isGuest) || view !== 'dashboard') return;

    let intervalId;
    let ticks = 0;

    const tick = () => {
        refreshData(true);
        ticks++;
        if (ticks % 3 === 0 && activeRepoRef.current) {
             fetch(`/api/repos/${activeRepoRef.current.name}`)
                .then(res => {
                    if (res.ok) return res.json();
                    throw new Error("Failed to fetch repo");
                })
                .then(updatedRepo => {
                     if (activeRepoRef.current && activeRepoRef.current.name === updatedRepo.name) {
                         setActiveRepo(updatedRepo);
                     }
                     setRepos(prevRepos => prevRepos.map(r => r.name === updatedRepo.name ? updatedRepo : r));
                })
                .catch(err => console.error("Failed to refresh active repo:", err));
        }
    };

    const start = () => {
      if (!intervalId) {
        intervalId = setInterval(tick, 20000);
      }
    };

    const stop = () => {
      if (intervalId) {
        clearInterval(intervalId);
        intervalId = null;
      }
    };

    const onVisibilityChange = () => {
      if (document.hidden) {
        stop();
      } else {
        tick(); // Update immediately when visible
        start();
      }
    };

    refreshData(false);

    if (!document.hidden) {
      start();
    }

    document.addEventListener("visibilitychange", onVisibilityChange);

    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [refreshData, isAuthenticated, isGuest, view]);

  const handleLogin = (scope) => {
    window.location.href = `/api/auth/login?scope=${scope}`;
  };

  const handleGuestLogin = () => {
    setIsGuest(true);
  };

  const handleLogout = () => {
    if (isGuest) {
        setIsGuest(false);
        setRepos([]);
        setActiveRepo(null);
        return;
    }
    fetch('/api/auth/logout', { method: 'POST' })
      .then(() => {
        setIsAuthenticated(false);
        setUser(null);
        setRepos([]);
        setActiveRepo(null);
      })
      .catch(err => console.error("Failed to logout", err));
  };

  const handleRepoClick = (repoName, currentRepos = repos) => {
    setView('dashboard');
    const repo = currentRepos.find(r => r.name === repoName);
    setActiveRepo(repo);
    setPrs([]);
    setIssues([]);
    setDevSandboxes([]);
    if (repo) {
      if (repo.review) {
        setActiveSubTab({ repo: repoName, name: 'review' });
      } else if (repo.issue) {
        setActiveSubTab({ repo: repoName, name: 'issues' });
      } else if (repo.dev) {
        setActiveSubTab({ repo: repoName, name: 'dev' });
      }
    }
  };

  const handleRepoDeleted = (deletedRepoName) => {
    fetchRepos();
    if (activeRepo && activeRepo.name === deletedRepoName) {
      setActiveRepo(null);
    }
  };

  const handleAddPR = (prId = null) => {
    let prNumber;
    
    if (prId) {
        prNumber = parseInt(prId);
    } else {
        const input = window.prompt("Enter PR URL or Number:");
        if (!input) return;

        prNumber = parseInt(input);
        if (isNaN(prNumber)) {
          // Try to parse URL
          // e.g., https://github.com/owner/repo/pull/123
          try {
            const url = new URL(input);
            const parts = url.pathname.split('/');
            const pullIndex = parts.indexOf('pull');
            if (pullIndex !== -1 && pullIndex + 1 < parts.length) {
                prNumber = parseInt(parts[pullIndex + 1]);
            }
          } catch (e) {
            // ignore
          }
        }
    }

    if (isNaN(prNumber) || !prNumber) {
        alert("Invalid PR number or URL");
        return;
    }

    fetch(`/api/repos/${activeRepo.name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ addPR: prNumber })
    })
    .then(res => {
        if (res.ok) {
            alert(`PR #${prNumber} has been added to watch list. It may take a few moments to appear.`);
            fetchRepos(); // Refresh repos to update lists
        } else {
            res.json().then(data => {
                const errorMsg = data.error || res.statusText;
                const hint = "\n\nTip: If this is a private repo or organization-restricted, you may need a manual GitHub Classic PAT with 'repo' permissions in 'Settings'.";
                alert("Failed to add PR: " + errorMsg + hint);
            });
        }
    })
    .catch(err => console.error("Failed to add PR:", err));
  };

  const handleDelete = (id) => {
    fetch(`/api/repo/${activeRepo.name}/prs/${id}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) {
           // Optimistically remove from view immediately
           setPrs(prevPrs => prevPrs.filter(pr => pr.id !== id));

           // Trigger exclusion in background
           fetch(`/api/repos/${activeRepo.name}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ excludePR: parseInt(id) })
           }).then(res2 => {
               if (res2.ok) {
                    fetchRepos();
               } else {
                   console.error("Sandbox deleted but failed to exclude PR");
               }
           });
           
           // Show alert slightly deferred to allow UI render
           setTimeout(() => {
                alert("PR Sandbox deleted. It will disappear from the list shortly.");
           }, 50);
        } else {
          alert("Failed to delete PR sandbox");
        }
      })
      .catch(err => console.error("Failed to delete PR:", err));
  };

  const handleSaveDraft = (id) => {
    const draft = yaml.dump(drafts[id]);
    fetch(`/api/repo/${activeRepo.name}/prs/${id}/draft`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft })
    }).catch(err => console.error("Failed to save draft:", err));
  };

  const handleDraftChange = (id, field, value, index = null) => {
    setDrafts(prevDrafts => {
      const newDraft = { ...prevDrafts[id] };
      if (field === 'note') {
        newDraft.note = value;
      } else if (field === 'review.body') {
        newDraft.review = { ...newDraft.review, body: value };
      } else if (field === 'comment.body' && index !== null) {
        newDraft.review.comments[index] = { ...newDraft.review.comments[index], body: value };
      } else if (field === 'comment.path' && index !== null) {
        newDraft.review.comments[index] = { ...newDraft.review.comments[index], path: value };
      } else if (field === 'comment.line' && index !== null) {
        newDraft.review.comments[index] = { ...newDraft.review.comments[index], line: value };
      } else if (field === 'comment.side' && index !== null) {
        newDraft.review.comments[index] = { ...newDraft.review.comments[index], side: value };
      }
      return { ...prevDrafts, [id]: newDraft };
    });
  };

  const handleRemoveComment = (id, index) => {
    setDrafts(prevDrafts => {
      const newDraft = { ...prevDrafts[id] };
      newDraft.review.comments.splice(index, 1);
      return { ...prevDrafts, [id]: newDraft };
    });
  };

  const handleIssueDraftChange = (issueId, value) => {
    setDrafts(prevDrafts => ({
      ...prevDrafts,
      [issueId]: value
    }));
  };

  const toggleReviewView = (id) => {
    const currentMode = reviewViewModes[id] || 'structured';
    if (currentMode === 'yaml') {
      try {
        const parsedDraft = yaml.load(yamlDrafts[id]);
        setDrafts(prev => ({ ...prev, [id]: parsedDraft }));
        setReviewViewModes(prev => ({ ...prev, [id]: 'structured' }));
      } catch (e) {
        alert('Invalid YAML. Please fix it before switching view.');
        console.error("YAML parse error on view switch:", e);
      }
    } else {
      setYamlDrafts(prev => ({ ...prev, [id]: yaml.dump(drafts[id] || { note: '', review: { body: '', comments: [] } }) }));
      setReviewViewModes(prev => ({ ...prev, [id]: 'yaml' }));
    }
  };

  const handleYamlDraftChange = (id, value) => {
    setYamlDrafts(prev => ({ ...prev, [id]: value }));
  };

  const handleYamlDraftBlur = (id) => {
    try {
      const parsedDraft = yaml.load(yamlDrafts[id]);
      setDrafts(prev => ({ ...prev, [id]: parsedDraft }));
      const draft = yaml.dump(parsedDraft);
      fetch(`/api/repo/${activeRepo.name}/prs/${id}/draft`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ draft })
      }).catch(err => console.error("Failed to save draft:", err));
    } catch (e) {
      alert('Invalid YAML, not saving.');
      console.error("YAML parse error on blur:", e);
    }
  };

  const handleSubmit = (id) => {
    let review;
    if (reviewViewModes[id] === 'yaml') {
      try {
        review = yaml.load(yamlDrafts[id]);
      } catch (e) {
        alert('Invalid YAML. Please fix it before submitting.');
        return;
      }
    } else {
      review = drafts[id];
    }

    if (!review || (!review.review.body?.trim() && (!review.review.comments || review.review.comments.length === 0))) {
      alert("Please leave a review comment before Submitting.");
      return;
    }
    const reviewYAML = yaml.dump(review);
    fetch(`/api/repo/${activeRepo.name}/prs/${id}/submitreview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ review: reviewYAML })
    })
    .then(res => {
      if (res.ok) {
        setPrs(prs.map(pr => pr.id === id ? { ...pr, review: reviewYAML, draft: '' } : pr));
      } else {
        res.json().then(data => {
            const errorMsg = data.error || res.statusText;
            const details = data.details ? "\nDetails: " + data.details : "";
            const hint = "\n\nTip: This often happens if the GitHub token has insufficient permissions for this organization. Go to 'Settings' and provide a manual GitHub Classic PAT with 'repo' (read/write) permissions.";
            alert("Failed to submit PR review: " + errorMsg + details + hint);
        });
      }
    })
    .catch(err => console.error("Failed to submit PR review:", err));
  };

  const handleExportCurl = (id, onSuccess) => {
    let review;
    if (reviewViewModes[id] === 'yaml') {
      try {
        review = yaml.load(yamlDrafts[id]);
      } catch (e) {
        alert('Invalid YAML. Please fix it before exporting.');
        return;
      }
    } else {
      review = drafts[id];
    }

    if (!review || (!review.review.body?.trim() && (!review.review.comments || review.review.comments.length === 0))) {
      alert("Please leave a review comment before Exporting.");
      return;
    }

    try {
      const url = new URL(activeRepo.url);
      const pathParts = url.pathname.split('/').filter(p => p);
      if (pathParts.length < 2) {
        alert("Invalid repo URL format");
        return;
      }
      const owner = pathParts[0];
      const repoName = pathParts[1];

      const reviewRequest = review.review;
      // Ensure event is not set (draft)
      const requestBody = { ...reviewRequest };
      delete requestBody.event;

      // Filter out null values from comments
      if (requestBody.comments) {
        requestBody.comments = requestBody.comments.map(comment => {
          const cleanComment = {};
          Object.keys(comment).forEach(key => {
            if (comment[key] !== null && comment[key] !== undefined) {
              cleanComment[key] = comment[key];
            }
          });
          return cleanComment;
        });
      }

      const jsonBody = JSON.stringify(requestBody);
      // Escape single quotes for bash single-quoted string using unicode escape
      const escapedJSONBody = jsonBody.replace(/'/g, '\\u0027');

      const curlCmd = `curl -L \\
  -X POST \\
  -H "Accept: application/vnd.github+json" \\
  -H "Authorization: Bearer <YOUR_TOKEN>" \\
  -H "X-GitHub-Api-Version: 2022-11-28" \\
  https://api.github.com/repos/${owner}/${repoName}/pulls/${id}/reviews \\
  -d '${escapedJSONBody}'`;

      if (onSuccess) {
        onSuccess(curlCmd);
      }
    } catch (e) {
      console.error("Failed to generate curl command:", e);
      alert("Failed to generate curl command: " + e.message);
    }
  };

  const handleIssueSaveDraft = (issueId) => {
    const draft = drafts[issueId];
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/draft`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft })
    }).catch(err => console.error("Failed to save issue draft:", err));
  };

  const handleIssueSubmit = (issueId) => {
    const comment = drafts[issueId];
    if (!comment.trim()) {
      alert("Please leave a comment before Submitting.");
      return;
    }
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/submitcomment`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ comment })
    })
    .then(res => {
      if (res.ok) {
        setIssues(issues.map(issue => issue.id === issueId ? { ...issue, comment, draft: '' } : issue));
      } else {
        res.json().then(data => {
            const errorMsg = data.error || res.statusText;
            const details = data.details ? "\nDetails: " + data.details : "";
            const hint = "\n\nTip: This often happens if the GitHub token has insufficient permissions for this organization. Go to 'Settings' and provide a manual GitHub Classic PAT with 'repo' (read/write) permissions.";
            alert("Failed to submit issue comment: " + errorMsg + details + hint);
        });
      }
    })
    .catch(err => console.error("Failed to submit issue comment:", err));
  };

  const handleIssueDelete = (issueId) => {
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) {
          // Optimistically remove from view immediately
          setIssues(issues.filter(issue => issue.id !== issueId));

          // Trigger exclusion in background
          // We pick the first handler to add exclusion to, or 'triage' if available
          let handlerName = '';
          if (activeRepo.issue && activeRepo.issue.handlers && activeRepo.issue.handlers.length > 0) {
              handlerName = activeRepo.issue.handlers[0].name;
          }
          
          if (handlerName) {
            fetch(`/api/repos/${activeRepo.name}`, {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ excludeIssue: parseInt(issueId), handlerName: handlerName })
            }).then(res2 => {
                if (res2.ok) {
                    fetchRepos();
                } else {
                    console.error("Sandbox deleted but failed to exclude Issue");
                }
            });
          }
          // Show alert slightly deferred to allow UI render
          setTimeout(() => {
               alert("Issue Sandbox deleted. It will disappear from the list shortly.");
          }, 50);
        } else {
          alert("Failed to delete issue sandbox");
        }
      })
      .catch(err => console.error("Failed to delete issue:", err));
  };

  const handleMoveCommentAndSave = (id, index, newPath, newLine, newSide) => {
    setDrafts(prevDrafts => {
      const newDrafts = JSON.parse(JSON.stringify(prevDrafts));
      const draftToUpdate = newDrafts[id];
      if (draftToUpdate && draftToUpdate.review && draftToUpdate.review.comments && draftToUpdate.review.comments[index]) {
        const commentToUpdate = draftToUpdate.review.comments[index];
        commentToUpdate.path = newPath;
        commentToUpdate.line = newLine;
        commentToUpdate.side = newSide;

        const draftYaml = yaml.dump(draftToUpdate);
        fetch(`/api/repo/${activeRepo.name}/prs/${id}/draft`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ draft: draftYaml })
        }).catch(err => console.error("Failed to save draft:", err));
      } else {
        console.error("Could not find comment to update in handleMoveCommentAndSave");
      }
      return newDrafts;
    });
  };

  const getSandboxStatusClass = (item) => {
    if (!item.sandbox) {
      return 'grey';
    }
    if (item.sandboxReplica === "0") {
      return 'yellow';
    }
    return 'green';
  };

  const toggleTheme = () => {
    setTheme(theme === 'light' ? 'dark' : 'light');
  };

  const toggleCollapse = (id) => {
    setCollapsedReviews(prev => ({
      ...prev,
      [id]: !prev[id]
    }));
  };

  const handlePRScaleUp = (id, manual = false) => {
    fetch(`/api/repo/${activeRepo.name}/prs/${id}/scaleup`, { 
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ manual })
    })
      .then(res => {
        if (res.ok) {
          // Refresh PRs to update status
          fetchRepos();
        } else {
          alert("Failed to scale up sandbox");
        }
      })
      .catch(err => console.error("Failed to scale up sandbox:", err));
  };

  const handlePRScaleDown = (id) => {
    fetch(`/api/repo/${activeRepo.name}/prs/${id}/scaledown`, { method: 'POST' })
      .then(res => {
        if (res.ok) {
          // Refresh PRs to update status
          fetchRepos();
        } else {
          alert("Failed to scale down sandbox");
        }
      })
      .catch(err => console.error("Failed to scale down sandbox:", err));
  };

  const handleIssueScaleUp = (issueId, manual = false) => {
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/scaleup`, { 
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ manual })
    })
      .then(res => {
        if (res.ok) {
          // Refresh Issues
          fetchRepos(); // This might be overkill but ensures consistency. Ideally we just re-fetch issues.
        } else {
          alert("Failed to scale up issue sandbox");
        }
      })
      .catch(err => console.error("Failed to scale up issue sandbox:", err));
  };

  const handleIssueScaleDown = (issueId) => {
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/scaledown`, { method: 'POST' })
      .then(res => {
        if (res.ok) {
           fetchRepos();
        } else {
          alert("Failed to scale down issue sandbox");
        }
      })
      .catch(err => console.error("Failed to scale down issue sandbox:", err));
  };

  const handleDevDelete = (sandbox) => {
    const sandboxName = sandbox.name;
    const branchName = sandbox.branch;


    fetch(`/api/repo/${activeRepo.name}/dev/${sandboxName}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) {
            // Optimistically remove from view
            setDevSandboxes(devSandboxes.filter(s => s.name !== sandboxName));

            // Trigger exclusion in background
            fetch(`/api/repos/${activeRepo.name}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ excludeBranch: branchName })
            })
            .then(res2 => {
                 if (res2.ok) {
                 } else {
                     console.error("Dev sandbox deleted but failed to exclude branch");
                 }
            });

           // Show alert slightly deferred to allow UI render
           setTimeout(() => {
                alert("Dev Sandbox deleted. It will disappear from the list shortly.");
           }, 50);
        } else {
            res.json().then(data => {
                alert("Failed to delete dev sandbox: " + (data.error || res.statusText));
            }).catch(() => {
                alert("Failed to delete dev sandbox");
            });
        }
      })
      .catch(err => {
          console.error("Failed to delete dev sandbox:", err);
      });
  };

  const handleDevScaleUp = (sandboxName) => {
    fetch(`/api/repo/${activeRepo.name}/dev/${sandboxName}/scaleup`, { method: 'POST' })
        .then(res => {
            if (res.ok) {
                fetchRepos(); // Refresh to get updated status
            } else {
                alert("Failed to scale up dev sandbox");
            }
        })
        .catch(err => console.error("Failed to scale up dev sandbox:", err));
  };

  const handleDevScaleDown = (sandboxName) => {
      fetch(`/api/repo/${activeRepo.name}/dev/${sandboxName}/scaledown`, { method: 'POST' })
          .then(res => {
              if (res.ok) {
                  fetchRepos(); // Refresh to get updated status
              } else {
                  alert("Failed to scale down dev sandbox");
              }
          })
          .catch(err => console.error("Failed to scale down dev sandbox:", err));
  };

  const handleDevCreate = (data) => {
      fetch(`/api/repo/${activeRepo.name}/dev`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(data)
      })
      .then(res => {
          if (res.ok) {
              fetchRepos(); // Refresh to show new sandbox
          } else {
              res.json().then(data => alert("Failed to create dev sandbox: " + data.error));
          }
      })
      .catch(err => console.error("Failed to create dev sandbox:", err));
  };

  const submitDevCreate = () => {
    if (newDevBranch) {
        handleDevCreate({ branch: newDevBranch, prompt: newDevPrompt });
        setDevModalOpen(false);
        setNewDevBranch('');
        setNewDevPrompt('');
    }
  };

  const handleFeedbackClick = async () => {
    try {
      const stream = await navigator.mediaDevices.getDisplayMedia({
        video: { cursor: "always" },
        audio: false
      });
      const video = document.createElement("video");
      video.srcObject = stream;
      video.onloadedmetadata = () => {
        video.play();
        const canvas = document.createElement("canvas");
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        const ctx = canvas.getContext("2d");
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        const image = canvas.toDataURL("image/png");
        setFeedbackImage(image);
        setFeedbackModalOpen(true);
        stream.getTracks().forEach(track => track.stop());

        // Automatically open image in new tab
        const w = window.open("");
        if (w) {
            w.document.write('<img src="' + image + '" style="max-width: 100%;" />');
            // Attempt to keep focus on current window (background the new tab)
            try {
                w.blur();
                window.focus();
            } catch (e) {
                // ignore
            }
        }
      };
    } catch (err) {
      console.error("Error capturing screen:", err);
      // Fallback to text only if user cancels or error
      setFeedbackImage('');
      setFeedbackModalOpen(true);
    }
  };

  const submitFeedback = () => {
    setIsSubmittingFeedback(true);
    fetch('/api/feedback', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: feedbackTitle, text: feedbackText, image: feedbackImage })
    })
    .then(res => {
        if (res.ok) {
            res.json().then(data => {
                alert(`Feedback submitted successfully!`);
                if (data.issue_url) {
                    window.open(data.issue_url, '_blank');
                }
                setFeedbackModalOpen(false);
                setFeedbackTitle('');
                setFeedbackText('');
                setFeedbackImage('');
            });
        } else {
            res.json().then(data => alert("Failed to submit feedback: " + (data.error || res.statusText)));
        }
    })
    .catch(err => alert("Failed to submit feedback: " + err))
    .finally(() => setIsSubmittingFeedback(false));
  };

  const renderContent = () => {
    if (!activeRepo) return <p>Please select or add a repository to watch.</p>;
    const namespace = user || 'default';
    if (activeSubTab.name === 'review') {
      return (
        <Review
          activeRepo={activeRepo}
          prs={prs}
          drafts={drafts}
          collapsedReviews={collapsedReviews}
          reviewViewModes={reviewViewModes}
          yamlDrafts={yamlDrafts}
          handleDelete={handleDelete}
          handleSaveDraft={handleSaveDraft}
          handleDraftChange={handleDraftChange}
          handleRemoveComment={handleRemoveComment}
          toggleReviewView={toggleReviewView}
          handleYamlDraftChange={handleYamlDraftChange}
          handleYamlDraftBlur={handleYamlDraftBlur}
          handleSubmit={handleSubmit}
          handleExportCurl={handleExportCurl}
          getSandboxStatusClass={getSandboxStatusClass}
          toggleCollapse={toggleCollapse}
          namespace={namespace}
          handleMoveCommentAndSave={handleMoveCommentAndSave}
          handleScaleUp={handlePRScaleUp}
          handleScaleDown={handlePRScaleDown}
          handleAddPR={handleAddPR}
          lastUpdated={lastUpdated}
        />
      );
    } else if (activeSubTab.name === 'dev') {
        const activeList = devSandboxes.map(sandbox => ({...sandbox, type: 'active'}));
        
        // Pending Branches
        const pending = activeRepo.pendingDevBranches || [];
        const pendingList = [];
        pending.forEach(branch => {
            if (!activeList.find(s => s.branch === branch)) {
                pendingList.push({ branch: branch, type: 'pending', name: branch }); // Name used for key/display
            }
        });

        // Excluded Branches
        const excluded = activeRepo.excludeBranches || [];
        const excludedList = [];
        excluded.forEach(branch => {
             if (!activeList.find(s => s.branch === branch)) {
                 excludedList.push({ branch: branch, type: 'excluded', name: branch });
             }
        });

        const handleAddDevInstance = (branch) => {
             setNewDevBranch(branch);
             setDevModalOpen(true);
        };

        const renderDevItem = (sandbox) => {
             if (sandbox.type === 'pending' || sandbox.type === 'excluded') {
                 return (
                     <div key={sandbox.name} className="pr-card" style={{opacity: 0.7}}>
                        <div className="pr-card-header">
                            <h3>{sandbox.branch} {sandbox.type === 'excluded' && '(Excluded)'}</h3>
                            <div className="pr-card-actions-header">
                                <button className="btn" onClick={() => handleAddDevInstance(sandbox.branch)} title="Create Dev Sandbox" style={{fontSize: '20px', width: '40px', height: '40px', borderRadius: '20px', lineHeight: '20px'}}>+</button>
                            </div>
                        </div>
                     </div>
                 );
             }
             return (
                <DevCard
                    key={sandbox.name}
                    sandbox={sandbox}
                    handleDelete={handleDevDelete}
                    getSandboxStatusClass={getSandboxStatusClass}
                    namespace={namespace}
                    handleScaleUp={handleDevScaleUp}
                    handleScaleDown={handleDevScaleDown}
                />
            );
        };

        const list = activeList.length === 0 && pendingList.length === 0 && excludedList.length === 0 ? <p>No active Dev Sandboxes found.</p> : (
            <>
                {activeList.map(renderDevItem)}
                
                {pendingList.length > 0 && (
                    <>
                        <h3 style={{marginTop: '30px', borderBottom: '1px solid var(--border-color)', paddingBottom: '10px', color: 'var(--text-secondary)'}}>Next ...</h3>
                        {pendingList.map(renderDevItem)}
                    </>
                )}

                {excludedList.length > 0 && (
                    <>
                        <h3 style={{marginTop: '30px', borderBottom: '1px solid var(--border-color)', paddingBottom: '10px', color: 'var(--text-secondary)'}}>Excluded...</h3>
                        {excludedList.map(renderDevItem)}
                    </>
                )}
            </>
        );

        return (
            <>
                {list}
                <div style={{textAlign: 'center', marginTop: '20px'}}>
                    <button className="btn" onClick={() => { setNewDevBranch(''); setDevModalOpen(true); }} title="Create new Dev Sandbox" style={{fontSize: '24px', width: '50px', height: '50px', borderRadius: '25px', lineHeight: '24px'}}>+</button>
                </div>
                {devModalOpen && (
                  <div className="modal-overlay" onClick={() => setDevModalOpen(false)}>
                      <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                          <h4>New Dev Sandbox Task</h4>
                          <input type="text" placeholder="New Branch Name" value={newDevBranch} onChange={(e) => setNewDevBranch(e.target.value)} style={{padding: '5px', border: '1px solid #ccc'}} />
                          <textarea placeholder="Prompt" value={newDevPrompt} onChange={(e) => setNewDevPrompt(e.target.value)} rows="15" style={{padding: '5px', border: '1px solid #ccc'}} />
                          <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px'}}>
                              <button className="btn" onClick={() => setDevModalOpen(false)} style={{backgroundColor: '#ccc', color: 'black'}}>Cancel</button>
                              <button className="btn" onClick={submitDevCreate} style={{backgroundColor: '#007bff', color: 'white'}}>Create</button>
                          </div>
                      </div>
                  </div>
                )}
            </>
        );
    } else if (activeSubTab.name === 'issues') {
      return (
        <Issues
          activeRepo={activeRepo}
          issues={issues}
          drafts={drafts}
          activeSubTab={activeSubTab}
          handleIssueDraftChange={handleIssueDraftChange}
          handleIssueSaveDraft={handleIssueSaveDraft}
          handleIssueSubmit={handleIssueSubmit}
          handleIssueDelete={handleIssueDelete}
          getSandboxStatusClass={getSandboxStatusClass}
          namespace={namespace}
          handleScaleUp={handleIssueScaleUp}
          handleScaleDown={handleIssueScaleDown}
        />
      );
    }
  };

  const renderDashboard = () => (
    <>
      <nav className="repo-tabs">
        {repos.map(repo => (
          <button
            key={repo.name}
            className={`tab-btn ${activeRepo && activeRepo.name === repo.name ? 'active' : ''}`}
            onClick={() => handleRepoClick(repo.name)}
          >
            {repo.name}
          </button>
        ))}
        <button className="tab-btn add-repo-btn" onClick={() => {
          if (!isGeminiKeySet && !isGuest) {
            alert("Please set your Gemini API Key in Settings before adding a repository.");
            setView('settings');
          } else {
            setView('add_repo');
          }
        }} title="Watch new repository">+</button>
      </nav>
      {activeRepo && (
        <div className="active-repo-container">
            <nav className="sub-tabs">
            {repos.find(r => r.name === activeRepo.name)?.review && (
                <button
                className={`sub-tab-btn ${activeSubTab.name === 'review' ? 'active' : ''}`}
                onClick={() => setActiveSubTab({ repo: activeRepo.name, name: 'review' })}
                >
                Review
                </button>
            )}
            {repos.find(r => r.name === activeRepo.name)?.issue && (
                <button
                className={`sub-tab-btn ${activeSubTab.name === 'issues' ? 'active' : ''}`}
                onClick={() => setActiveSubTab({ repo: activeRepo.name, name: 'issues' })}
                >
                Issues
                </button>
            )}
            {repos.find(r => r.name === activeRepo.name)?.dev && (
                <button
                className={`sub-tab-btn ${activeSubTab.name === 'dev' ? 'active' : ''}`}
                onClick={() => setActiveSubTab({ repo: activeRepo.name, name: 'dev' })}
                >
                Dev
                </button>
            )}
            </nav>
            <div className="repo-controls">
                {activeRepo.review?.assignees && activeRepo.review.assignees.length > 0 && (
                    <span className="assignee-filter" title={`Watching PRs assigned to: ${activeRepo.review.assignees.join(', ')}`}>
                        Filter: {activeRepo.review.assignees.join(', ')}
                    </span>
                )}
                <button className="btn btn-refresh-lg" onClick={() => refreshData(true)} title="Refresh now">↻</button>
                {lastUpdated && <span className={`last-updated ${Date.now() - lastUpdated > 60000 ? 'stale' : ''}`}>Updated {lastUpdated.toLocaleTimeString()}</span>}
                <button className="btn" onClick={() => setView('update_repo')} style={{marginLeft: '10px', marginRight: '10px'}}>
                    Repo Settings
                    {hasInstructionDraft && <span style={{marginLeft: '5px', color: '#ffcc00', fontWeight: 'bold'}}>●</span>}
                </button>
            </div>
        </div>
      )}
      <main className={activeSubTab.name === 'review' ? 'pr-list-review' : 'pr-list'}>
        {renderContent()}
      </main>
    </>
  );

  if (isLoadingAuth) return <div className="App"><header className="App-header"><h1>Loading...</h1></header></div>;

  if (!isAuthenticated && !isGuest) {
    return (
      <div className="App">
        <header className="App-header">
          <h1>Repo Agent</h1>
          <div className="theme-switch-wrapper">
            <label className="theme-switch" htmlFor="checkbox"><input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} /><div className="slider round"></div></label>
          </div>
        </header>
        <main className="login-container">
          <h2>Welcome to Repo Agent</h2>
          <div className="login-actions">
            {githubAuthEnabled ? (
                <>
                <button className="btn btn-submit" onClick={() => handleLogin('readwrite')} style={{backgroundColor: '#0366d6', marginRight: '10px'}}>Login with GitHub (Read-Write)</button>
                <button className="btn btn-submit" onClick={() => handleLogin('readonly')} style={{backgroundColor: '#6f42c1'}}>Login with GitHub (Read-Only)</button>
                </>
            ) : (
                <button className="btn btn-submit" onClick={handleGuestLogin}>Continue</button>
            )}
          </div>
        </main>
      </div>
    );
  }

  return (
    <div className="App">
      <header className="App-header">
        <h1><a href="/" onClick={(e) => { e.preventDefault(); setView('dashboard'); }}>Repo Agent</a></h1>
        <div className="header-right">
          {user && <span className="user-greeting">Hi, {user}</span>}
          {isGuest && <span className="user-greeting">Guest</span>}
          <button className="btn" onClick={handleFeedbackClick} style={{marginRight: '10px', backgroundColor: '#28a745'}}>Feedback</button>
          <button className="btn" onClick={() => setView('settings')} style={{marginRight: '10px'}}>Settings</button>
          <button className="btn btn-delete" onClick={handleLogout} style={{marginRight: '20px'}}>Logout</button>
          <div className="theme-switch-wrapper">
            <label className="theme-switch" htmlFor="checkbox"><input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} /><div className="slider round"></div></label>
          </div>
        </div>
      </header>
      
      {(isAuthenticated || isGuest) && !isGeminiKeySet && (
        <div className="warning-banner">
          <strong>⚠️ Gemini API Key Missing:</strong> Please configure your Gemini API Key in <a href="#" onClick={(e) => { e.preventDefault(); setView('settings'); }}>Settings</a> to enable code reviews and issue handling.
        </div>
      )}
      
      {view === 'dashboard' && renderDashboard()}
      {view === 'settings' && <Settings onBack={() => setView('dashboard')} />}
      {view === 'add_repo' && <AddRepo onCancel={() => setView('dashboard')} onRepoAdded={() => { fetchRepos(); setView('dashboard'); }} />}
      {view === 'update_repo' && <UpdateRepo repo={activeRepo} onCancel={() => setView('dashboard')} onRepoUpdated={() => { fetchRepos(); setView('dashboard'); }} onRepoDeleted={handleRepoDeleted} />}

      {feedbackModalOpen && (
        <div className="modal-overlay" onClick={() => setFeedbackModalOpen(false)}>
            <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{maxHeight: '90vh', overflowY: 'auto'}}>
                <h4>Send Feedback</h4>
                {feedbackImage && (
                    <>
                        <div style={{border: '1px solid #ccc', padding: '5px', maxHeight: '300px', overflow: 'hidden'}}>
                            <img 
                                src={feedbackImage} 
                                alt="Screenshot" 
                                style={{maxWidth: '100%', display: 'block', cursor: 'pointer'}} 
                                title="Click to open in new tab"
                                onClick={() => {
                                    const w = window.open("");
                                    if (w) {
                                        w.document.write('<img src="' + feedbackImage + '" style="max-width: 100%;" />');
                                    }
                                }}
                            />
                        </div>
                        <p style={{fontSize: '0.9em', color: '#555', marginTop: '5px', marginBottom: '5px'}}>
                           <strong>Note:</strong> The screenshot has been opened in a new tab. You can also click the image above to open it again. Please copy and manually paste the screenshot into the GitHub issue that will be created after you click "Send Feedback".
                        </p>
                    </>
                )}
                <input 
                    type="text" 
                    placeholder="Title" 
                    value={feedbackTitle} 
                    onChange={(e) => setFeedbackTitle(e.target.value)} 
                    style={{padding: '5px', border: '1px solid #ccc'}} 
                />
                <textarea 
                    placeholder="Describe your issue or feedback..." 
                    value={feedbackText} 
                    onChange={(e) => setFeedbackText(e.target.value)} 
                    rows="5" 
                    style={{padding: '5px', border: '1px solid #ccc'}} 
                />
                <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px'}}>
                    <button className="btn" onClick={() => setFeedbackModalOpen(false)} style={{backgroundColor: '#ccc', color: 'black'}}>Cancel</button>
                    <button className="btn" onClick={submitFeedback} disabled={isSubmittingFeedback} style={{backgroundColor: '#007bff', color: 'white'}}>
                        {isSubmittingFeedback ? 'Sending...' : 'Send Feedback'}
                    </button>
                </div>
            </div>
        </div>
      )}

    </div>
  );
}

export default App;
