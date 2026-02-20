import React, { useState, useEffect, useCallback, useRef } from 'react';
import yaml from 'js-yaml';
import './App.css';
import PrReviewCard from './PrReviewCard';
import Review from './Review';
import Issues from './Issues';
import IssueCard from './IssueCard';
import DevCard from './DevCard';
import ExplorationGroup from './ExplorationGroup';
import DevSidebar from './DevSidebar';
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
  const [activeSandbox, setActiveSandbox] = useState(null); // Selected sandbox in Dev Tab
  const [drafts, setDrafts] = useState({});
  const [collapsedReviews, setCollapsedReviews] = useState({});
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'light');
  const [reviewViewModes, setReviewViewModes] = useState({});
  const [yamlDrafts, setYamlDrafts] = useState({});
  const [lastUpdated, setLastUpdated] = useState(null);
  const [hasInstructionDraft, setHasInstructionDraft] = useState(false);
  const [isRefreshing, setIsRefreshing] = useState(false);
  
  // Dev Sandbox Sidebar State
  const [sidebarWidth, setSidebarWidth] = useState(400);
  const [isResizing, setIsResizing] = useState(false);

  // Dev Sandbox Modals
  const [devModalOpen, setDevModalOpen] = useState(false);
  const [newDevBranch, setNewDevBranch] = useState('');
  const [newDevPrompt, setNewDevPrompt] = useState('');
  
  // Exploration Modals
  const [explorationModalOpen, setExplorationModalOpen] = useState(false);
  const [newExplorationIdea, setNewExplorationIdea] = useState('');
  const [newExplorationDescription, setNewExplorationDescription] = useState('');

  // Approach Modal
  const [approachModalOpen, setApproachModalOpen] = useState(false);
  const [targetIdeaID, setTargetIdeaID] = useState('');
  const [newApproachName, setNewApproachName] = useState('');
  const [newApproachPrompt, setNewApproachPrompt] = useState('');
  const [baseBranchForFork, setBaseBranchForFork] = useState('');
  const [parentApproachForFork, setParentApproachForFork] = useState('');

  const [feedbackModalOpen, setFeedbackModalOpen] = useState(false);
  const [feedbackTitle, setFeedbackTitle] = useState('');
  const [feedbackText, setFeedbackText] = useState('');
  const [feedbackImage, setFeedbackImage] = useState('');
  const [isSubmittingFeedback, setIsSubmittingFeedback] = useState(false);

  // Toast notifications (replacing alert())
  const [toasts, setToasts] = useState([]);
  const showToast = useCallback((message, type = 'info') => {
    const id = Date.now();
    setToasts(prev => [...prev, { id, message, type }]);
    setTimeout(() => setToasts(prev => prev.filter(t => t.id !== id)), 4000);
  }, []);

  const dismissToast = useCallback((id) => {
    setToasts(prev => prev.filter(t => t.id !== id));
  }, []);

  // Prompt modal (replacing window.prompt for Add PR)
  const [promptModalOpen, setPromptModalOpen] = useState(false);
  const [promptInput, setPromptInput] = useState('');

  // Confirm modal (replacing window.confirm)
  const [confirmModal, setConfirmModal] = useState({ open: false, message: '', onConfirm: null, onCancel: null });
  const showConfirm = useCallback((message) => {
    return new Promise((resolve) => {
      setConfirmModal({
        open: true,
        message,
        onConfirm: () => { setConfirmModal({ open: false, message: '', onConfirm: null, onCancel: null }); resolve(true); },
        onCancel: () => { setConfirmModal({ open: false, message: '', onConfirm: null, onCancel: null }); resolve(false); },
      });
    });
  }, []);


  useEffect(() => {
    document.body.className = theme === 'dark' ? 'dark-mode' : '';
    localStorage.setItem('theme', theme);
  }, [theme]);

  // Sidebar Resizing Logic
  const startResizing = useCallback(() => {
    setIsResizing(true);
  }, []);

  const stopResizing = useCallback(() => {
    setIsResizing(false);
  }, []);

  const resize = useCallback(
    (mouseMoveEvent) => {
      if (isResizing) {
        // Limit width to reasonable bounds
        const newWidth = Math.max(200, Math.min(mouseMoveEvent.clientX, 600));
        setSidebarWidth(newWidth);
      }
    },
    [isResizing]
  );

  useEffect(() => {
    window.addEventListener("mousemove", resize);
    window.addEventListener("mouseup", stopResizing);
    return () => {
      window.removeEventListener("mousemove", resize);
      window.removeEventListener("mouseup", stopResizing);
    };
  }, [resize, stopResizing]);

  // Keep activeSandbox in sync with polled updates
  useEffect(() => {
      if (activeSandbox && devSandboxes.length > 0) {
          const updated = devSandboxes.find(s => s.name === activeSandbox.name);
          if (updated && (updated.agentState !== activeSandbox.agentState || updated.sandboxReplica !== activeSandbox.sandboxReplica)) {
              setActiveSandbox(updated);
          }
      }
  }, [devSandboxes, activeSandbox]);

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
    setLastUpdated(null);
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
    if (prId) {
        submitAddPR(String(prId));
    } else {
        setPromptInput('');
        setPromptModalOpen(true);
    }
  };

  const submitAddPR = (input) => {
    let prNumber = parseInt(input);
    if (isNaN(prNumber)) {
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

    if (isNaN(prNumber) || !prNumber) {
        showToast("Invalid PR number or URL", 'error');
        return;
    }

    fetch(`/api/repos/${activeRepo.name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ addPR: prNumber })
    })
    .then(res => {
        if (res.ok) {
            showToast(`PR #${prNumber} has been added to watch list. It may take a few moments to appear.`, 'success');
            fetchRepos();
        } else {
            res.json().then(data => {
                const errorMsg = data.error || res.statusText;
                showToast("Failed to add PR: " + errorMsg, 'error');
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
           
           showToast("PR Sandbox deleted. It will disappear from the list shortly.", 'success');
        } else {
          showToast("Failed to delete PR sandbox", 'error');
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
        showToast('Invalid YAML. Please fix it before switching view.', 'error');
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
      showToast('Invalid YAML, not saving.', 'error');
      console.error("YAML parse error on blur:", e);
    }
  };

  const handleSubmit = (id) => {
    let review;
    if (reviewViewModes[id] === 'yaml') {
      try {
        review = yaml.load(yamlDrafts[id]);
      } catch (e) {
        showToast('Invalid YAML. Please fix it before submitting.', 'error');
        return;
      }
    } else {
      review = drafts[id];
    }

    if (!review || (!review.review.body?.trim() && (!review.review.comments || review.review.comments.length === 0))) {
      showToast("Please leave a review comment before Submitting.", 'info');
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
            const details = data.details ? " Details: " + data.details : "";
            showToast("Failed to submit PR review: " + errorMsg + details, 'error');
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
        showToast('Invalid YAML. Please fix it before exporting.', 'error');
        return;
      }
    } else {
      review = drafts[id];
    }

    if (!review || (!review.review.body?.trim() && (!review.review.comments || review.review.comments.length === 0))) {
      showToast("Please leave a review comment before Exporting.", 'info');
      return;
    }

    try {
      const url = new URL(activeRepo.url);
      const pathParts = url.pathname.split('/').filter(p => p);
      if (pathParts.length < 2) {
        showToast("Invalid repo URL format", 'error');
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
      showToast("Failed to generate curl command: " + e.message, 'error');
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
      showToast("Please leave a comment before Submitting.", 'info');
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
            const details = data.details ? " Details: " + data.details : "";
            showToast("Failed to submit issue comment: " + errorMsg + details, 'error');
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
          showToast("Issue Sandbox deleted. It will disappear from the list shortly.", 'success');
        } else {
          showToast("Failed to delete issue sandbox", 'error');
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
    if (item.sandboxStatus && (item.sandboxStatus === 'Evicted' || item.sandboxStatus.startsWith('fail:') || item.sandboxStatus === 'Failed')) {
      return 'red';
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
          showToast("Failed to scale up sandbox", 'error');
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
          showToast("Failed to scale down sandbox", 'error');
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
          showToast("Failed to scale up issue sandbox", 'error');
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
          showToast("Failed to scale down issue sandbox", 'error');
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

           showToast("Dev Sandbox deleted. It will disappear from the list shortly.", 'success');
        } else {
            res.json().then(data => {
                showToast("Failed to delete dev sandbox: " + (data.error || res.statusText), 'error');
            }).catch(() => {
                showToast("Failed to delete dev sandbox", 'error');
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
                showToast("Failed to scale up dev sandbox", 'error');
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
                  showToast("Failed to scale down dev sandbox", 'error');
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
              res.json().then(data => showToast("Failed to create dev sandbox: " + data.error, 'error'));
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

  const submitExplorationCreate = () => {
    if (newExplorationIdea) {
        // Idea ID should be URL-safe-ish
        const ideaID = newExplorationIdea.toLowerCase().replace(/[^a-z0-9-]/g, '-');
        
        handleDevCreate({ 
            ideaID: ideaID, 
            description: newExplorationDescription
        });
        setExplorationModalOpen(false);
        setNewExplorationIdea('');
        setNewExplorationDescription('');
    } else {
        showToast("Idea Name is required.", 'info');
    }
  };

  const submitApproachCreate = () => {
      if (targetIdeaID && newApproachName) {
          const approach = newApproachName.toLowerCase().replace(/[^a-z0-9-]/g, '-');
          const payload = {
              ideaID: targetIdeaID,
              approach: approach,
              prompt: newApproachPrompt
          };
          
          if (baseBranchForFork) {
              payload.baseBranch = baseBranchForFork;
              payload.parentApproach = parentApproachForFork;
          }

          handleDevCreate(payload);
          setApproachModalOpen(false);
          setTargetIdeaID('');
          setNewApproachName('');
          setNewApproachPrompt('');
          setBaseBranchForFork('');
          setParentApproachForFork('');
      } else {
          showToast("Approach Name is required.", 'info');
      }
  };

  const handleOpenAddApproach = (ideaID) => {
      setTargetIdeaID(ideaID);
      setNewApproachName('');
      setNewApproachPrompt('');
      setBaseBranchForFork('');
      setParentApproachForFork('');
      setApproachModalOpen(true);
  };
  
  const handleForkDevInstance = (sandbox) => {
      if (sandbox.ideaID) {
        setTargetIdeaID(sandbox.ideaID);
        // Pre-fill name if it follows a pattern, or leave blank?
        // Let's leave blank but maybe we can suggest something in the placeholder
        setNewApproachName(''); 
        setNewApproachPrompt('');
        setBaseBranchForFork(sandbox.branch);
        setParentApproachForFork(sandbox.approach || sandbox.branch);
        setApproachModalOpen(true);
      } else {
          // If forking an ungrouped sandbox, maybe start a new exploration based on it?
          // For now, let's just alert not supported or implement basic branching
          showToast("Forking ungrouped sandboxes into new explorations is not yet supported via this button.", 'info');
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
                showToast("Feedback submitted successfully!", 'success');
                if (data.issue_url) {
                    window.open(data.issue_url, '_blank');
                }
                setFeedbackModalOpen(false);
                setFeedbackTitle('');
                setFeedbackText('');
                setFeedbackImage('');
            });
        } else {
            res.json().then(data => showToast("Failed to submit feedback: " + (data.error || res.statusText), 'error'));
        }
    })
    .catch(err => showToast("Failed to submit feedback: " + err, 'error'))
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
          onRefresh={() => refreshData(true)}
          showToast={showToast}
        />
      );
    } else if (activeSubTab.name === 'dev') {
        const activeList = devSandboxes.map(sandbox => ({...sandbox, type: 'active'}));
        
        // Group by Idea ID
        const explorations = {};
        const ungrouped = [];

        activeList.forEach(sandbox => {
            if (sandbox.ideaID) {
                if (!explorations[sandbox.ideaID]) {
                    explorations[sandbox.ideaID] = [];
                }
                explorations[sandbox.ideaID].push(sandbox);
            } else {
                ungrouped.push(sandbox);
            }
        });

        // Ensure activeSandbox is still up to date with new data
        if (activeSandbox) {
            const updatedActive = activeList.find(s => s.name === activeSandbox.name);
            if (updatedActive && updatedActive !== activeSandbox) {
                // Only update if reference changed to avoid loop, though React handles set state check
                // We use a useEffect/callback pattern for this usually, but inside render we just rely on data being fresh
            }
        }

        const handleAddDevInstance = (branch) => {
             setNewDevBranch(branch);
             setDevModalOpen(true);
        };

        return (
            <div className="dev-layout">
                <div style={{ width: sidebarWidth, display: 'flex', flexDirection: 'column' }}>
                    <DevSidebar 
                        explorations={explorations}
                        ungrouped={ungrouped}
                        activeSandbox={activeSandbox}
                        onSelectSandbox={setActiveSandbox}
                        onAddExploration={() => setExplorationModalOpen(true)}
                        onAddApproach={handleOpenAddApproach}
                    />
                </div>
                <div 
                    className="resizer"
                    onMouseDown={startResizing}
                />
                
                <div className="dev-main">
                    {activeSandbox ? (
                        <DevCard
                            key={activeSandbox.name}
                            sandbox={activeSandbox}
                            handleDelete={handleDevDelete}
                            getSandboxStatusClass={getSandboxStatusClass}
                            namespace={namespace}
                            handleScaleUp={handleDevScaleUp}
                            handleScaleDown={handleDevScaleDown}
                            handleFork={handleForkDevInstance}
                            repoName={activeRepo.name}
                            showToast={showToast}
                        />
                    ) : (
                        <div style={{textAlign: 'center', marginTop: '50px', color: 'var(--text-secondary)'}}>
                            <p>Select an approach from the sidebar to view details.</p>
                            <p>Or create a standalone sandbox:</p>
                            <button className="btn" onClick={() => { setNewDevBranch(''); setDevModalOpen(true); }} title="Create new Dev Sandbox (Branch)">
                                Create Standalone Sandbox
                            </button>
                        </div>
                    )}

                    {/* Modals are rendered here to be part of the main layout but absolute/fixed positioned */}
                    {devModalOpen && (
                    <div className="modal-overlay" onClick={() => setDevModalOpen(false)}>
                        <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                            <h4>New Dev Sandbox (Branch)</h4>
                            <input type="text" placeholder="Branch Name" value={newDevBranch} onChange={(e) => setNewDevBranch(e.target.value)} style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} />
                            <textarea placeholder="Prompt (optional)" value={newDevPrompt} onChange={(e) => setNewDevPrompt(e.target.value)} rows="15" style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} />
                            <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px'}}>
                                <button className="btn" onClick={() => setDevModalOpen(false)} style={{backgroundColor: 'var(--bg-secondary)', color: 'var(--text-primary)'}}>Cancel</button>
                                <button className="btn" onClick={submitDevCreate} style={{backgroundColor: 'var(--color-primary)', color: 'white'}}>Create</button>
                            </div>
                        </div>
                    </div>
                    )}

                    {explorationModalOpen && (
                        <div className="modal-overlay" onClick={() => setExplorationModalOpen(false)}>
                            <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                                <h4>Start New Exploration</h4>
                                <div className="form-group">
                                    <label>Exploration Name (e.g., optimize-db)</label>
                                    <input type="text" value={newExplorationIdea} onChange={(e) => setNewExplorationIdea(e.target.value)} style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} />
                                </div>
                                <div className="form-group">
                                    <label>Description</label>
                                    <textarea value={newExplorationDescription} onChange={(e) => setNewExplorationDescription(e.target.value)} rows="5" style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} />
                                </div>
                                <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px', marginTop: '15px'}}>
                                    <button className="btn" onClick={() => setExplorationModalOpen(false)} style={{backgroundColor: 'var(--bg-secondary)', color: 'var(--text-primary)'}}>Cancel</button>
                                    <button className="btn" onClick={submitExplorationCreate} style={{backgroundColor: 'var(--color-primary)', color: 'white'}}>Create Exploration</button>
                                </div>
                            </div>
                        </div>
                    )}

                    {approachModalOpen && (
                        <div className="modal-overlay" onClick={() => setApproachModalOpen(false)}>
                            <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                                <h4>Add Approach to {targetIdeaID}</h4>
                                <div className="form-group">
                                    <label>Approach Name (e.g., attempt-2)</label>
                                    <input type="text" value={newApproachName} onChange={(e) => setNewApproachName(e.target.value)} style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} />
                                </div>
                                <div className="form-group">
                                    <label>Prompt (Instructions for Agent)</label>
                                    <textarea value={newApproachPrompt} onChange={(e) => setNewApproachPrompt(e.target.value)} rows="10" style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} />
                                </div>
                                <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px', marginTop: '15px'}}>
                                    <button className="btn" onClick={() => setApproachModalOpen(false)} style={{backgroundColor: 'var(--bg-secondary)', color: 'var(--text-primary)'}}>Cancel</button>
                                    <button className="btn" onClick={submitApproachCreate} style={{backgroundColor: 'var(--color-primary)', color: 'white'}}>Create Approach</button>
                                </div>
                            </div>
                        </div>
                    )}
                </div>
            </div>
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
          lastUpdated={lastUpdated}
          showToast={showToast}
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
            showToast("Please set your Gemini API Key in Settings before adding a repository.", 'info');
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
                onClick={() => { setLastUpdated(null); setActiveSubTab({ repo: activeRepo.name, name: 'review' }); }}
                >
                <span className="material-symbols-outlined">rate_review</span>
                Review
                </button>
            )}
            {repos.find(r => r.name === activeRepo.name)?.issue && (
                <button
                className={`sub-tab-btn ${activeSubTab.name === 'issues' ? 'active' : ''}`}
                onClick={() => { setLastUpdated(null); setActiveSubTab({ repo: activeRepo.name, name: 'issues' }); }}
                >
                <span className="material-symbols-outlined">bug_report</span>
                Issues
                </button>
            )}
            {repos.find(r => r.name === activeRepo.name)?.dev && (
                <button
                className={`sub-tab-btn ${activeSubTab.name === 'dev' ? 'active' : ''}`}
                onClick={() => { setLastUpdated(null); setActiveSubTab({ repo: activeRepo.name, name: 'dev' }); }}
                >
                <span className="material-symbols-outlined">code</span>
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
                <button className="btn btn-refresh-lg" onClick={() => { setIsRefreshing(true); refreshData(true); setTimeout(() => setIsRefreshing(false), 1000); }} title="Refresh now">
                    <span className={`material-symbols-outlined ${isRefreshing ? 'spinning' : ''}`} style={{fontSize: '18px'}}>refresh</span>
                </button>
                {lastUpdated && <span className={`last-updated ${Date.now() - lastUpdated > 60000 ? 'stale' : ''}`}>Updated {lastUpdated.toLocaleTimeString()}</span>}
                <button className="btn" onClick={() => setView('update_repo')} style={{marginLeft: '10px', marginRight: '10px'}}>
                    Repo Settings
                    {hasInstructionDraft && <span style={{marginLeft: '5px', color: '#ffcc00', fontWeight: 'bold'}}>●</span>}
                </button>
            </div>
        </div>
      )}
      <main className={activeSubTab.name === 'review' ? 'pr-list-review' : (activeSubTab.name === 'dev' ? 'dev-container-full' : 'pr-list')}>
        {renderContent()}
      </main>
    </>
  );

  if (isLoadingAuth) return (
    <div className="App">
      <div className="loading-screen">
        <div className="loading-spinner"></div>
        <p style={{color: 'var(--text-muted)', fontSize: '14px', fontWeight: 500}}>Loading Repo Agent...</p>
      </div>
    </div>
  );

  if (!isAuthenticated && !isGuest) {
    return (
      <div className="App">
        <div className="login-page-wrapper">
          <main className="login-container">
            <div className="login-logo">
              <div className="login-logo-icon">
                <span className="material-symbols-outlined">terminal</span>
              </div>
            </div>
            <h2 style={{fontSize: '24px', fontWeight: 700, letterSpacing: '-0.02em', marginBottom: '4px'}}>Repo Agent</h2>
            <p className="login-tagline">AI-powered repository management</p>
            <div className="login-actions">
              {githubAuthEnabled ? (
                  <>
                  <button className="btn btn-submit" onClick={() => handleLogin('readwrite')} style={{width: '100%', justifyContent: 'center', padding: '12px 16px', boxShadow: '0 4px 14px var(--color-primary-glow)'}}>
                    <span className="material-symbols-outlined" style={{fontSize: '20px'}}>code</span>
                    Sign in with GitHub (Read-Write)
                  </button>
                  <button className="btn btn-secondary" onClick={() => handleLogin('readonly')} style={{width: '100%', justifyContent: 'center', padding: '12px 16px'}}>
                    <span className="material-symbols-outlined" style={{fontSize: '20px'}}>visibility</span>
                    Sign in with GitHub (Read-Only)
                  </button>
                  </>
              ) : (
                  <button className="btn btn-submit" onClick={handleGuestLogin} style={{width: '100%', justifyContent: 'center', padding: '12px 16px'}}>Continue</button>
              )}
            </div>
            <div style={{marginTop: '24px'}}>
              <div className="theme-switch-wrapper" style={{justifyContent: 'center'}}>
                <label className="theme-switch" htmlFor="checkbox"><input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} /><div className="slider round"></div></label>
              </div>
            </div>
          </main>
          <div style={{marginTop: '32px', display: 'flex', justifyContent: 'center', gap: '16px', fontSize: '12px', color: 'var(--text-muted)'}}>
            <span>Powered by AI</span>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="App">
      <header className="App-header">
        <div style={{display: 'flex', alignItems: 'center'}}>
          <h1 style={{display: 'flex', alignItems: 'center'}}>
            <a href="/" onClick={(e) => { e.preventDefault(); setView('dashboard'); }} style={{display: 'flex', alignItems: 'center'}}>
              <span className="header-logo-icon"><span className="material-symbols-outlined">terminal</span></span>
              Repo Agent
            </a>
          </h1>
        </div>
        <div className="header-right">
          <button className="header-icon-btn" onClick={handleFeedbackClick} title="Send Feedback">
            <span className="material-symbols-outlined">feedback</span>
          </button>
          <button className="header-icon-btn" onClick={() => setView('settings')} title="Settings">
            <span className="material-symbols-outlined">settings</span>
          </button>
          <button className="header-icon-btn" onClick={toggleTheme} title="Toggle theme">
            <span className="material-symbols-outlined">{theme === 'dark' ? 'light_mode' : 'dark_mode'}</span>
          </button>
          <button className="header-icon-btn" onClick={handleLogout} title="Logout" style={{color: 'var(--text-danger)'}}>
            <span className="material-symbols-outlined">logout</span>
          </button>
          <div className="header-avatar" title={user || (isGuest ? 'Guest' : '')}>
            {(user || 'G').substring(0, 2).toUpperCase()}
          </div>
        </div>
      </header>
      
      {(isAuthenticated || isGuest) && !isGeminiKeySet && (
        <div className="warning-banner">
          <strong>⚠️ Gemini API Key Missing:</strong> Please configure your Gemini API Key in <a href="#" onClick={(e) => { e.preventDefault(); setView('settings'); }}>Settings</a> to enable code reviews and issue handling.
        </div>
      )}

      {activeRepo && activeRepo.conditions && activeRepo.conditions.filter(c => c.status === 'False').map((c, i) => (
        <div key={i} className="warning-banner" style={{ backgroundColor: 'rgba(239,68,68,0.1)', color: 'var(--status-red)', borderColor: 'rgba(239,68,68,0.2)' }}>
          <strong>⚠️ {c.type}:</strong> {c.message} <span style={{ opacity: 0.7, fontSize: 'small' }}>({c.reason}{c.lastTransitionTime ? ` — ${new Date(c.lastTransitionTime).toLocaleString()}` : ''})</span>
        </div>
      ))}

      {view === 'dashboard' && renderDashboard()}
      {view === 'settings' && <Settings onBack={() => setView('dashboard')} showConfirm={showConfirm} />}
      {view === 'add_repo' && <AddRepo onCancel={() => setView('dashboard')} onRepoAdded={() => { fetchRepos(); setView('dashboard'); }} />}
      {view === 'update_repo' && <UpdateRepo repo={activeRepo} onCancel={() => setView('dashboard')} onRepoUpdated={() => { fetchRepos(); setView('dashboard'); }} onRepoDeleted={handleRepoDeleted} showConfirm={showConfirm} />}

      {feedbackModalOpen && (
        <div className="modal-overlay" onClick={() => setFeedbackModalOpen(false)}>
            <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{maxHeight: '90vh', overflowY: 'auto'}}>
                <h4>Send Feedback</h4>
                {feedbackImage && (
                    <>
                        <div style={{border: '1px solid var(--border-color)', padding: '5px', maxHeight: '300px', overflow: 'hidden', borderRadius: '8px'}}>
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
                        <p style={{fontSize: '12px', color: 'var(--text-muted)', marginTop: '5px', marginBottom: '5px'}}>
                           <strong>Note:</strong> The screenshot has been opened in a new tab. You can also click the image above to open it again. Please copy and manually paste the screenshot into the GitHub issue that will be created after you click "Send Feedback".
                        </p>
                    </>
                )}
                <input 
                    type="text" 
                    placeholder="Title" 
                    value={feedbackTitle} 
                    onChange={(e) => setFeedbackTitle(e.target.value)} 
                    style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} 
                />
                <textarea 
                    placeholder="Describe your issue or feedback..." 
                    value={feedbackText} 
                    onChange={(e) => setFeedbackText(e.target.value)} 
                    rows="5" 
                    style={{padding: '12px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)'}} 
                />
                <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px'}}>
                    <button className="btn" onClick={() => setFeedbackModalOpen(false)} style={{backgroundColor: 'var(--bg-secondary)', color: 'var(--text-primary)'}}>Cancel</button>
                    <button className="btn" onClick={submitFeedback} disabled={isSubmittingFeedback} style={{backgroundColor: 'var(--color-primary)', color: 'white'}}>
                        {isSubmittingFeedback ? 'Sending...' : 'Send Feedback'}
                    </button>
                </div>
            </div>
        </div>
      )}

      <footer style={{ textAlign: 'center', padding: '10px', marginTop: '20px', color: 'var(--text-muted)', fontSize: '12px' }}>
        Repo Agent UI {process.env.REACT_APP_GIT_SHA ? `(${process.env.REACT_APP_GIT_SHA.substring(0, 7)})` : ''}
      </footer>

      {/* Prompt Modal (Add PR) */}
      {promptModalOpen && (
        <div className="modal-overlay" onClick={() => setPromptModalOpen(false)}>
          <div className="confirm-modal-content" onClick={(e) => e.stopPropagation()}>
            <h4 style={{margin: 0}}>Add PR</h4>
            <p>Enter a PR URL or number:</p>
            <input
              type="text"
              value={promptInput}
              onChange={(e) => setPromptInput(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') { setPromptModalOpen(false); submitAddPR(promptInput); } }}
              placeholder="e.g. 123 or https://github.com/owner/repo/pull/123"
              autoFocus
              style={{padding: '12px', border: '1px solid var(--border-color-input)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)', fontSize: '14px', fontFamily: 'var(--font-ui)'}}
            />
            <div className="confirm-modal-actions">
              <button className="btn btn-secondary" onClick={() => setPromptModalOpen(false)}>Cancel</button>
              <button className="btn btn-submit" onClick={() => { setPromptModalOpen(false); submitAddPR(promptInput); }}>Add</button>
            </div>
          </div>
        </div>
      )}

      {/* Confirm Modal */}
      {confirmModal.open && (
        <div className="modal-overlay" onClick={confirmModal.onCancel}>
          <div className="confirm-modal-content" onClick={(e) => e.stopPropagation()}>
            <h4 style={{margin: 0}}>Confirm</h4>
            <p>{confirmModal.message}</p>
            <div className="confirm-modal-actions">
              <button className="btn btn-secondary" onClick={confirmModal.onCancel}>Cancel</button>
              <button className="btn btn-submit" onClick={confirmModal.onConfirm}>Confirm</button>
            </div>
          </div>
        </div>
      )}

      {/* Toast Notifications */}
      <div className="toast-container">
        {toasts.map(t => (
          <div key={t.id} className={`toast ${t.type}`} onClick={() => dismissToast(t.id)}>
            <span className="material-symbols-outlined" style={{fontSize:'16px'}}>
              {t.type === 'success' ? 'check_circle' : t.type === 'error' ? 'error' : 'info'}
            </span>
            {t.message}
          </div>
        ))}
      </div>

    </div>
  );
}

export default App;
