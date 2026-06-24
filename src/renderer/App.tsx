import { Outlet } from 'react-router-dom';

/**
 * 应用布局壳：仅作为路由出口。各页面自行管理快照订阅与错误提示。
 */
export function App() {
  return <Outlet />;
}
