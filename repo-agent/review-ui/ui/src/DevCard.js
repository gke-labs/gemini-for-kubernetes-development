import React from 'react';

function DevCard({
  sandbox,
  handleDelete,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
}) {

  return (
    <div key={sandbox.name} className="pr-card">
      <div className="pr-card-header">
        <h3>
          <a href={sandbox.branchURL} target="_blank" rel="noopener noreferrer">{sandbox.branch || sandbox.name}</a>
        </h3>
        <div className="pr-card-actions-header">
          {getSandboxStatusClass(sandbox) === 'green' ? (
            <div style={{display: 'flex', alignItems: 'center', gap: '5px'}}>
              <a href={`/sandbox/${namespace}/${sandbox.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(sandbox)}`}>
                Sandbox
              </a>
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
           <button className="btn btn-delete" onClick={(e) => { e.stopPropagation(); handleDelete(sandbox.name); }}>&#x2715;</button>
        </div>
      </div>
    </div>
  );
}

export default DevCard;
