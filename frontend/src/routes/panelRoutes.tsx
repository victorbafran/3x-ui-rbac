import { lazy } from 'react';
import type { ComponentType } from 'react';
import {
  ApiOutlined,
  ClusterOutlined,
  CrownOutlined,
  DashboardOutlined,
  ExportOutlined,
  GlobalOutlined,
  ImportOutlined,
  SettingOutlined,
  SwapOutlined,
  TagsOutlined,
  TeamOutlined,
  ToolOutlined,
  UserSwitchOutlined,
} from '@ant-design/icons';

const IndexPage = lazy(() => import('@/pages/index/IndexPage'));
const InboundsPage = lazy(() => import('@/pages/inbounds/InboundsPage'));
const ClientsPage = lazy(() => import('@/pages/clients/ClientsPage'));
const GroupsPage = lazy(() => import('@/pages/groups/GroupsPage'));
const NodesPage = lazy(() => import('@/pages/nodes/NodesPage'));
const RelaysPage = lazy(() => import('@/pages/relays/RelaysPage'));
const HostsPage = lazy(() => import('@/pages/hosts/HostsPage'));
const AdminsPage = lazy(() => import('@/pages/admins/AdminsPage'));
const SettingsPage = lazy(() => import('@/pages/settings/SettingsPage'));
const XrayPage = lazy(() => import('@/pages/xray/XrayPage'));
const ApiDocsPage = lazy(() => import('@/pages/api-docs/ApiDocsPage'));
const SponsorsPage = lazy(() => import('@/pages/sponsors/SponsorsPage'));

export interface PanelRoute {
  path: string;
  titleKey: string;
  icon: ComponentType;
  component: ComponentType;
  // The permission switch that shows the page; a route without one is shown to everyone.
  section?: string;
}

// The router, the sidebar, the page titles and the command palette all read this list, so a
// page cannot go missing from one of them.
export const PANEL_ROUTES: PanelRoute[] = [
  { path: '/', titleKey: 'menu.dashboard', icon: DashboardOutlined, component: IndexPage },
  {
    path: '/inbounds',
    titleKey: 'menu.inbounds',
    icon: ImportOutlined,
    component: InboundsPage,
    section: 'sections.inbounds',
  },
  {
    path: '/clients',
    titleKey: 'menu.clients',
    icon: TeamOutlined,
    component: ClientsPage,
    section: 'sections.clients',
  },
  {
    path: '/groups',
    titleKey: 'menu.groups',
    icon: TagsOutlined,
    component: GroupsPage,
    section: 'sections.groups',
  },
  {
    path: '/nodes',
    titleKey: 'menu.nodes',
    icon: ClusterOutlined,
    component: NodesPage,
    section: 'sections.nodes',
  },
  // Enrolling a relay hands the panel root on another machine, so the entry
  // is in no role's defaults and only a super admin ever sees it.
  {
    path: '/relays',
    titleKey: 'menu.relays',
    icon: ClusterOutlined,
    component: RelaysPage,
    section: 'sections.relays',
  },
  {
    path: '/hosts',
    titleKey: 'menu.hosts',
    icon: GlobalOutlined,
    component: HostsPage,
    section: 'sections.hosts',
  },
  {
    path: '/admins',
    titleKey: 'menu.admins',
    icon: UserSwitchOutlined,
    component: AdminsPage,
    section: 'sections.admins',
  },
  {
    path: '/outbound',
    titleKey: 'menu.outbounds',
    icon: ExportOutlined,
    component: XrayPage,
    section: 'sections.outbounds',
  },
  {
    path: '/routing',
    titleKey: 'menu.routing',
    icon: SwapOutlined,
    component: XrayPage,
    section: 'sections.routing',
  },
  {
    path: '/settings',
    titleKey: 'menu.settings',
    icon: SettingOutlined,
    component: SettingsPage,
    section: 'sections.settings',
  },
  {
    path: '/xray',
    titleKey: 'menu.xray',
    icon: ToolOutlined,
    component: XrayPage,
    section: 'sections.xray',
  },
  { path: '/api-docs', titleKey: 'menu.apiDocs', icon: ApiOutlined, component: ApiDocsPage },
  { path: '/sponsors', titleKey: 'menu.sponsors', icon: CrownOutlined, component: SponsorsPage },
];

export const PANEL_ROUTE_PATHS = PANEL_ROUTES.map((route) => route.path);

export const PANEL_TITLE_KEYS: Record<string, string> = Object.fromEntries(
  PANEL_ROUTES.map((route) => [route.path, route.titleKey]),
);
