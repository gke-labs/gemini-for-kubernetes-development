jest.mock('react-markdown', () => ({ children }) => children);
jest.mock('remark-gfm', () => 'gfm-plugin-stub');
jest.mock('xterm', () => ({ Terminal: class { open() {} write() {} dispose() {} onData() {} loadAddon() {} } }));
jest.mock('xterm-addon-fit', () => ({ FitAddon: class { fit() {} } }));

const { taskSessionRoute } = require('./App');

// A popped-out session: the board's, or an overseer's on its routes.
test('taskSessionRoute', () => {
    expect(taskSessionRoute('#/task-session/fix-r-5/recipe-fix-1')).toEqual({ sandbox: 'fix-r-5', task: 'recipe-fix-1', apiBase: '/api' });
    expect(taskSessionRoute('#/overseers/kcc/task-session/fix-r-5/recipe-fix-1')).toEqual({ sandbox: 'fix-r-5', task: 'recipe-fix-1', apiBase: '/api/overseers/kcc' });
    expect(taskSessionRoute('#/overseers/kcc')).toBeNull();
    // The Files tab, and a file open in it.
    expect(taskSessionRoute('#/overseers/kcc/task-session/fix-r-5/recipe-fix-1?tab=files')).toEqual({ sandbox: 'fix-r-5', task: 'recipe-fix-1', apiBase: '/api/overseers/kcc', tab: 'files' });
    expect(taskSessionRoute('#/overseers/kcc/task-session/fix-r-5/recipe-fix-1?tab=files&file=runs%2Frun1__REPORT.md')).toEqual({ sandbox: 'fix-r-5', task: 'recipe-fix-1', apiBase: '/api/overseers/kcc', tab: 'files', file: 'runs/run1__REPORT.md' });
});
