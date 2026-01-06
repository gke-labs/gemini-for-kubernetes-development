import React, { useState, useEffect } from 'react';

function DevCard({
  sandbox,
  handleDelete,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
}) {
  const [flairText, setFlairText] = useState('');

  const getFlairColor = (text) => {
    if (!text) return '#cd9945ff';
    const lower = text.toLowerCase();
    if (lower === 'ready') return 'green';
    if (lower.includes('provisioning')) return '#2196F3';
    if (lower.includes('error')) return '#9e2a2aff';
    return '#cd9945ff';
  };

  useEffect(() => {
    setFlairText(sandbox.agentState || '');
  }, [sandbox.agentState]);

  return (
    <div key={sandbox.name} className="pr-card">
      <div className="pr-card-header">
        <h3>
          <a href={sandbox.branchURL} target="_blank" rel="noopener noreferrer">{sandbox.branch || sandbox.name}</a>
        </h3>
        <div className="pr-card-actions-header">
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
              {sandbox.agentState === 'provisioning' ? (
                <span className="pr-sandbox" style={{backgroundColor: '#2196F3', color: 'white', cursor: 'default'}} title={sandbox.agentStateMessage || ''}>
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
          ) : (
            <span className={`pr-sandbox ${getSandboxStatusClass(sandbox)}`}>Sandbox: Not created</span>
          )}
           <button className="btn btn-delete" onClick={(e) => { e.stopPropagation(); handleDelete(sandbox); }}>&#x2715;</button>
        </div>
      </div>
    </div>
  );
}

export default DevCard;
