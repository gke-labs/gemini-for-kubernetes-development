import React, { useState, useEffect, useCallback, useRef } from 'react';
import './App.css';

const Overseer = ({ onBack }) => {
    const [overseers, setOverseers] = useState([]);
    const [activeOverseer, setActiveOverseer] = useState(null);
    const [chores, setChores] = useState([]);
    const [activeChore, setActiveChore] = useState(null);
    const [tasks, setTasks] = useState([]);
    const [activeTask, setActiveTask] = useState(null);
    const [logs, setLogs] = useState('');
    const [loading, setLoading] = useState(false);
    const [showOverseerLogs, setShowOverseerLogs] = useState(false);

    const logIntervalRef = useRef(null);

    const fetchOverseers = useCallback(() => {
        fetch('/api/overseers')
            .then(res => res.json())
            .then(data => {
                setOverseers(data || []);
                if (data && data.length > 0 && !activeOverseer) {
                    setActiveOverseer(data[0]);
                }
            })
            .catch(err => console.error("Failed to fetch overseers:", err));
    }, [activeOverseer]);

    useEffect(() => {
        fetchOverseers();
    }, [fetchOverseers]);

    const fetchChores = useCallback(() => {
        if (!activeOverseer) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores`)
            .then(res => res.json())
            .then(data => setChores(data || []))
            .catch(err => console.error("Failed to fetch chores:", err));
    }, [activeOverseer]);

    useEffect(() => {
        fetchChores();
        const interval = setInterval(fetchChores, 10000);
        return () => clearInterval(interval);
    }, [fetchChores]);

    const fetchOverseerLogs = useCallback(() => {
        if (!activeOverseer || !showOverseerLogs) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/logs`)
            .then(res => res.text())
            .then(data => setLogs(data))
            .catch(err => console.error("Failed to fetch overseer logs:", err));
    }, [activeOverseer, showOverseerLogs]);

    const fetchChoreTasks = useCallback(() => {
        if (!activeOverseer || !activeChore) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores/${activeChore.metadata.name}/tasks`)
            .then(res => res.json())
            .then(data => {
                setTasks(data || []);
                if (data && data.length > 0 && !activeTask) {
                    setActiveTask(data[0]);
                }
            })
            .catch(err => console.error("Failed to fetch chore tasks:", err));
    }, [activeOverseer, activeChore, activeTask]);

    const fetchChoreLogs = useCallback(() => {
        if (!activeOverseer || !activeChore || !activeTask) return;
        fetch(`/api/overseers/${activeOverseer.metadata.name}/chores/${activeChore.metadata.name}/logs?taskID=${activeTask.metadata.name}`)
            .then(res => res.text())
            .then(data => setLogs(data))
            .catch(err => console.error("Failed to fetch chore logs:", err));
    }, [activeOverseer, activeChore, activeTask]);

    useEffect(() => {
        if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        
        if (showOverseerLogs) {
            fetchOverseerLogs();
            logIntervalRef.current = setInterval(fetchOverseerLogs, 5000);
        } else if (activeChore && activeTask) {
            fetchChoreLogs();
            logIntervalRef.current = setInterval(fetchChoreLogs, 5000);
        } else {
            setLogs('');
        }

        return () => {
            if (logIntervalRef.current) clearInterval(logIntervalRef.current);
        };
    }, [showOverseerLogs, activeChore, activeTask, fetchOverseerLogs, fetchChoreLogs]);

    useEffect(() => {
        if (activeChore) {
            fetchChoreTasks();
            const interval = setInterval(fetchChoreTasks, 10000);
            return () => clearInterval(interval);
        }
    }, [activeChore, fetchChoreTasks]);

    const handleOverseerClick = (ov) => {
        setActiveOverseer(ov);
        setActiveChore(null);
        setActiveTask(null);
        setShowOverseerLogs(true);
    };

    const handleChoreClick = (chore) => {
        setActiveChore(chore);
        setActiveTask(null);
        setShowOverseerLogs(false);
    };

    return (
        <div className="dev-layout" style={{ height: 'calc(100vh - 80px)' }}>
            <div className="dev-sidebar" style={{ width: '300px', borderRight: '1px solid var(--border-color)' }}>
                <div className="sidebar-header-row">
                    <span className="sidebar-header-title">Overseer Repos</span>
                </div>
                <div className="sidebar-tree-content">
                    {overseers.map(ov => (
                        <div 
                            key={ov.metadata.name} 
                            className={`sidebar-tree-row root-row ${activeOverseer?.metadata.name === ov.metadata.name ? 'active' : ''}`}
                            onClick={() => handleOverseerClick(ov)}
                        >
                            <span className="tree-icon">📂</span>
                            <span className="tree-label">{ov.metadata.name}</span>
                        </div>
                    ))}
                </div>

                {activeOverseer && (
                    <>
                        <div className="sidebar-header-row" style={{ marginTop: '20px' }}>
                            <span className="sidebar-header-title">Components</span>
                        </div>
                        <div className="sidebar-tree-content">
                            <div 
                                className={`sidebar-tree-row ${showOverseerLogs ? 'active' : ''}`}
                                onClick={() => { setShowOverseerLogs(true); setActiveChore(null); }}
                            >
                                <span className="tree-icon">🤖</span>
                                <span className="tree-label">Overseer Agent</span>
                            </div>
                            <div className="sidebar-header" style={{ paddingLeft: '15px', marginTop: '10px' }}>Chores</div>
                            {chores.length === 0 && <div style={{ paddingLeft: '35px', fontSize: '0.8rem', color: 'var(--text-muted)' }}>No chores found</div>}
                            {chores.map(chore => (
                                <div 
                                    key={chore.metadata.name}
                                    className={`sidebar-tree-row ${activeChore?.metadata.name === chore.metadata.name ? 'active' : ''}`}
                                    onClick={() => handleChoreClick(chore)}
                                    style={{ paddingLeft: '25px' }}
                                >
                                    <span className="tree-icon">⚙️</span>
                                    <span className="tree-label">{chore.metadata.labels['chore.gemini.google.com/name'] || chore.metadata.name}</span>
                                </div>
                            ))}
                        </div>
                    </>
                )}
            </div>

            <div className="dev-main">
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '20px' }}>
                    <h2>
                        {showOverseerLogs ? `Overseer: ${activeOverseer?.metadata.name}` : `Chore: ${activeChore?.metadata.labels['chore.gemini.google.com/name'] || activeChore?.metadata.name}`}
                    </h2>
                    <button className="btn" onClick={onBack}>Back to Dashboard</button>
                </div>

                {!showOverseerLogs && activeChore && (
                    <div style={{ marginBottom: '20px' }}>
                        <h4>Tasks</h4>
                        <div style={{ display: 'flex', gap: '10px', flexWrap: 'wrap', marginBottom: '20px' }}>
                            {tasks.map(task => (
                                <button 
                                    key={task.metadata.name}
                                    className={`btn btn-sm ${activeTask?.metadata.name === task.metadata.name ? 'btn-submit' : ''}`}
                                    onClick={() => setActiveTask(task)}
                                    style={{ backgroundColor: activeTask?.metadata.name === task.metadata.name ? '' : 'var(--bg-secondary)', color: activeTask?.metadata.name === task.metadata.name ? '' : 'var(--text-primary)' }}
                                >
                                    {task.spec.type} ({new Date(task.metadata.creationTimestamp).toLocaleTimeString()})
                                </button>
                            ))}
                            {tasks.length === 0 && <p>No tasks found for this chore.</p>}
                        </div>
                    </div>
                )}

                <div className="logs-container">
                    <h4>Logs</h4>
                    <div className="logs-display" style={{ backgroundColor: '#1e1e1e', color: '#d4d4d4', padding: '15px', borderRadius: '5px', height: '600px', overflowY: 'auto', fontFamily: 'monospace', fontSize: '13px', lineHeight: '1.5' }}>
                        <pre style={{ margin: 0, whiteSpace: 'pre-wrap' }}>
                            {logs || 'Loading logs...'}
                        </pre>
                    </div>
                </div>
            </div>
        </div>
    );
};

export default Overseer;
