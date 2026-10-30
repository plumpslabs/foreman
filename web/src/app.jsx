import { useEffect, useRef, useState } from 'preact/hooks';
import { api, loadWorkspace } from './api.js';
import { HomePage, RoomsPage, RoomDetailHost, TeamsPage, AgentsPage, SkillsPage, RunsPage, SettingsPage, MCPServersPage } from './pages.jsx';
import { AgentDetailPage, TeamDetailPage, TeamCreatePage, SkillDetailPage } from './detail.jsx';
import { ModalHost } from './modals.jsx';
import { Icon } from './icons.jsx';

const NAV_PRIMARY = [
  ['home', 'Overview', 'layout-dashboard'],
  ['rooms', 'Rooms', 'messages-square'],
  ['teams', 'Teams', 'users'],
  ['agents', 'Agent library', 'bot'],
  ['skills', 'Skills', 'puzzle'],
  ['runs', 'Runs & activity', 'activity'],
];
const NAV_SECONDARY = [['mcp', 'MCP Servers', 'network'], ['settings', 'Settings', 'settings-2']];

const PARENT_PAGE = {
  'room-detail': 'rooms',
  'agent-detail': 'agents',
  'team-detail': 'teams',
  'team-create': 'teams',
  'skill-detail': 'skills',
};

const routeToHash = (page, id = '') => {
  const enc = (value) => encodeURIComponent(value);
  switch (page) {
    case 'home': return '#/';
    case 'room-detail': return `#/rooms/${enc(id)}`;
    case 'team-create': return '#/teams/new';
    case 'team-detail': return `#/teams/${enc(id)}`;
    case 'agent-detail': return id ? `#/agents/${enc(id)}` : '#/agents/new';
    case 'skill-detail': return id ? `#/skills/${enc(id)}` : '#/skills/new';
    default: return `#/${page}`;
  }
};

const hashToRoute = (hash) => {
  const parts = String(hash || '').replace(/^#\/?/, '').split('/').filter(Boolean);
  if (!parts.length) return { page: 'home', id: '' };
  const [head, id] = parts;
  const decode = (value) => { try { return decodeURIComponent(value); } catch { return value; } };
  switch (head) {
    case 'rooms':
      return id ? { page: 'room-detail', id: decode(id) } : { page: 'rooms', id: '' };
    case 'teams':
      if (id === 'new') return { page: 'team-create', id: '' };
      return id ? { page: 'team-detail', id: decode(id) } : { page: 'teams', id: '' };
    case 'agents':
      if (id === 'new') return { page: 'agent-detail', id: '' };
      return id ? { page: 'agent-detail', id: decode(id) } : { page: 'agents', id: '' };
    case 'skills':
      if (id === 'new') return { page: 'skill-detail', id: '' };
      return id ? { page: 'skill-detail', id: decode(id) } : { page: 'skills', id: '' };
    case 'runs': return { page: 'runs', id: '' };
    case 'settings': return { page: 'settings', id: '' };
    case 'mcp': return { page: 'mcp', id: '' };
    default: return { page: 'home', id: '' };
  }
};

const emptyState = {
  projectPath: '',
  rooms: [],
  teams: [],
  agents: [],
  templates: [],
  providers: [],
  tasks: [],
  runs: [],
  skills: [],
  settings: { theme: 'dark' },
};

export function App() {
  const initial = hashToRoute(typeof window !== 'undefined' ? window.location.hash : '');
  const [state, setState] = useState(null);
  const [page, setPage] = useState(initial.page);
  const [selectedId, setSelectedId] = useState(initial.id);
  const [modal, setModal] = useState(null);
  const [toast, setToast] = useState('');
  const toastTimer = useRef(0);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);

  const toggleSidebar = () => {
    if (typeof window !== 'undefined' && window.innerWidth <= 760) {
      setSidebarOpen((v) => !v);
    } else {
      setSidebarCollapsed((v) => !v);
    }
  };

  const notify = (message) => {
    setToast(message);
    clearTimeout(toastTimer.current);
    toastTimer.current = setTimeout(() => setToast(''), 2600);
  };

  const refresh = async () => {
    try {
      setState(await loadWorkspace());
    } catch (error) {
      notify(error.message);
    }
  };

  useEffect(() => { refresh(); }, []);

  useEffect(() => {
    const apply = () => {
      const next = hashToRoute(window.location.hash);
      setPage(next.page);
      setSelectedId(next.id);
      setSidebarOpen(false);
    };
    apply();
    window.addEventListener('hashchange', apply);
    return () => window.removeEventListener('hashchange', apply);
  }, []);

  const current = state || emptyState;
  const settings = current.settings || emptyState.settings;

  // Real-time updates via SSE: refresh whenever the server reports a state change.
  useEffect(() => {
    if (!current || !current.projectPath) return undefined;
    const url = `/api/events?project=${encodeURIComponent(current.projectPath)}`;
    let es;
    try {
      es = new EventSource(url);
      const handler = () => refresh();
      es.addEventListener('update', handler);
      es.onmessage = handler;
    } catch (e) {
      return undefined;
    }
    return () => { if (es) es.close(); };
  }, [current && current.projectPath]);
  const themeClass = settings.theme === 'light' ? 'theme-light' : '';

  const go = (nextPage, id = '') => {
    const hash = routeToHash(nextPage, id);
    if (window.location.hash === hash) {
      setPage(nextPage);
      setSelectedId(id);
    } else {
      window.location.hash = hash;
    }
    setSidebarOpen(false);
  };

  const navigate = (next) => go(next);
  const openRoom = (id) => go('room-detail', id);
  const openAgent = (id) => go('agent-detail', id);
  const openTeam = (id) => go('team-detail', id);
  const openSkill = (id) => go('skill-detail', id);
  const openTeamCreate = () => go('team-create');

  const runTask = async (taskId, button) => {
    if (button) button.disabled = true;
    try {
      await api(`/api/tasks/${encodeURIComponent(taskId)}/run`, { method: 'POST', body: '{}' });
      notify('Run started');
      await refresh();
    } catch (error) {
      notify(error.message);
    } finally {
      if (button) button.disabled = false;
    }
  };

  const deleteSkill = async (skillId) => {
    try {
      await api(`/api/skills/${encodeURIComponent(skillId)}`, { method: 'DELETE' });
      await refresh();
      notify('Skill deleted');
    } catch (error) {
      notify(error.message);
    }
  };

  const deleteAgent = async (agent) => {
    if (!confirm(`Hapus agent "${agent.name}" dari library global?\nTeam yang sudah memakainya tetap menyimpan salinannya.`)) return;
    try {
      await api(`/api/agents/${encodeURIComponent(agent.id)}`, { method: 'DELETE' });
      await refresh();
      notify('Agent dihapus dari library global');
    } catch (error) {
      notify(error.message);
    }
  };

  const deleteRoom = async (roomId) => {
    const room = current.rooms.find((r) => r.id === roomId);
    if (!confirm(`Delete room "${room?.name || roomId}" and all of its tasks and runs?\nThis cannot be undone.`)) return;
    try {
      await api(`/api/rooms/${encodeURIComponent(roomId)}`, { method: 'DELETE' });
      await refresh();
      notify('Room deleted');
    } catch (error) {
      notify(error.message);
    }
  };

  const cancelRun = async (runId) => {
    try {
      await api(`/api/runs/${encodeURIComponent(runId)}/cancel`, { method: 'POST', body: '{}' });
      notify('Cancellation requested');
      await refresh();
    } catch (error) {
      notify(error.message);
    }
  };

  const pauseRun = async (runId) => {
    try {
      await api(`/api/runs/${encodeURIComponent(runId)}/pause`, { method: 'POST', body: '{}' });
      notify('Stop requested — the agent will halt at a safe point');
      await refresh();
      setTimeout(() => refresh(), 1500);
    } catch (error) {
      notify(error.message);
    }
  };

  const importTeamTemplate = async (template) => {
    try {
      const team = await api('/api/teams/import', {
        method: 'POST',
        body: JSON.stringify({ templateId: template.id, name: template.name }),
      });
      await refresh();
      notify(`Team "${template.name}" diimpor — ${(template.agents || []).length} agent`);
      openTeam(team.id);
      return true;
    } catch (error) {
      notify(error.message);
      return false;
    }
  };

  const deleteTeam = async (teamId) => {
    try {
      await api(`/api/teams/${encodeURIComponent(teamId)}`, { method: 'DELETE' });
      await refresh();
      notify('Team dihapus');
      navigate('teams');
    } catch (error) {
      notify(error.message);
    }
  };

  const pageProps = {
    state: current, notify, refresh, setModal, navigate,
    openRoom, openAgent, openTeam, openSkill, openTeamCreate,
    onTeamCreate: openTeamCreate,
    onUseTemplate: importTeamTemplate,
    runTask, deleteSkill, cancelRun, deleteRoom,
    importTeamTemplate, deleteAgent, deleteTeam,
  };

  const pages = {
    home: <HomePage {...pageProps} />,
    rooms: <RoomsPage {...pageProps} />,
    teams: <TeamsPage {...pageProps} />,
    agents: <AgentsPage {...pageProps} />,
    skills: <SkillsPage {...pageProps} />,
    runs: <RunsPage {...pageProps} />,
    settings: <SettingsPage {...pageProps} />,
    mcp: <MCPServersPage {...pageProps} />,
  };

  const activeRoom = current.rooms.find((room) => room.id === selectedId);
  const activeAgent = (current.agents || []).find((agent) => agent.id === selectedId);
  const activeTeam = current.teams.find((team) => team.id === selectedId);
  const activeSkill = (current.skills || []).find((skill) => skill.id === selectedId);

  const parent = PARENT_PAGE[page] || page;
  const pageLabel = parent === 'home' ? 'Overview'
    : (NAV_PRIMARY.concat(NAV_SECONDARY).find(([key]) => key === parent) || ['home', 'Overview'])[1];

  const renderNav = (items) => items.map(([key, label, icon]) => (
    <button class={`nav-btn ${parent === key ? 'active' : ''}`} onClick={() => navigate(key)} title={label}>
      <Icon name={icon} />
      <span class="nav-label-text">{label}</span>
    </button>
  ));

  const renderContent = () => {
    if (state === null) return <div class="empty">Loading workspace…</div>;
    switch (page) {
      case 'room-detail':
        return activeRoom
          ? <RoomDetailHost room={activeRoom} {...pageProps} onBack={() => navigate('rooms')} />
          : <RoomsPage {...pageProps} />;
      case 'agent-detail':
        return <AgentDetailPage key={`agent-${selectedId || 'new'}`} agentId={selectedId} {...pageProps} onBack={() => navigate('agents')} />;
      case 'team-detail':
        return activeTeam
          ? <TeamDetailPage key={`team-${selectedId}`} teamId={selectedId} {...pageProps} onBack={() => navigate('teams')} onDelete={deleteTeam} />
          : <TeamsPage {...pageProps} />;
      case 'team-create':
        return (
          <TeamCreatePage
            {...pageProps}
            onBack={() => navigate('teams')}
            onCreated={(id) => openTeam(id)}
          />
        );
      case 'mcp':
        return <MCPServersPage {...pageProps} />;
      case 'skill-detail':
        return (
          <SkillDetailPage
            key={`skill-${selectedId || 'new'}`}
            skillId={selectedId}
            {...pageProps}
            onBack={() => navigate('skills')}
          />
        );
      default:
        return pages[page] || pages.home;
    }
  };

  const breadcrumbLabel = page === 'room-detail' && activeRoom ? activeRoom.name
    : page === 'agent-detail' ? (activeAgent ? activeAgent.name : 'New agent')
      : page === 'team-detail' && activeTeam ? activeTeam.name
        : page === 'team-create' ? 'Create team'
          : page === 'skill-detail' ? (activeSkill ? activeSkill.name : 'New skill')
            : pageLabel;

  return (
    <div class={`app ${themeClass} ${sidebarCollapsed ? 'collapsed' : ''}`}>
      <aside class={`sidebar ${sidebarOpen ? 'mobile-open' : ''}`} id="sidebar"
        onClick={(e) => { if (e.target.classList.contains('sidebar')) setSidebarOpen(false); }}>
        <div class="brand">
          <img src="./foreman.png" alt="Foreman" />
          <div>
            <div class="brand-name">foreman<span class="dot">.</span></div>
            <div class="brand-tag">Agent workspace</div>
          </div>
        </div>
        <nav class="nav" aria-label="Main navigation">
          <div class="nav-label">Workspace</div>
          {renderNav(NAV_PRIMARY)}
          <div class="nav-label">Configure</div>
          {renderNav(NAV_SECONDARY)}
        </nav>
        <div class="sidebar-bottom">
          <div class="project-switch">
            <div class="project-dot" />
            <div class="project-switch-text">
              <div class="project-switch-name">Local workspace</div>
              <div class="project-switch-path">{current.projectPath || 'Loading…'}</div>
            </div>
          </div>
        </div>
      </aside>
      <main class="main">
        <header class="topbar">
          <div style="display:flex;align-items:center;gap:10px;min-width:0">
            <button class="icon-btn mobile-menu" onClick={toggleSidebar}
              aria-label={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}>
              <Icon name="menu" />
            </button>
            <div class="breadcrumb">
              <Icon name="layout-dashboard" />
              <span>{pageLabel}</span>
              <Icon name="chevron-right" size={13} />
              <b>{breadcrumbLabel}</b>
            </div>
          </div>
          <div class="top-actions">
            {(current.approvals || []).filter((a) => a.status === 'pending').length > 0 && (
              <button
                class={`btn small top-approval-btn ${(current.approvals || []).some((a) => a.status === 'pending' && a.severity === 'blocker') ? 'sev-blocker' : 'warning'}`}
                style="display:flex;align-items:center;gap:6px;font-weight:600"
                onClick={() => setModal({ kind: 'approvals' })}
              >
                <Icon name="alert-triangle" size={13} />
                <span>Approvals ({(current.approvals || []).filter((a) => a.status === 'pending').length})</span>
              </button>
            )}
            <div class="status-pill"><span class="status-dot" />Runtime ready</div>
          </div>
        </header>
        <div class={`content ${['room-detail', 'agent-detail', 'team-detail', 'team-create', 'skill-detail'].includes(page) ? 'flush' : ''}`}>
          {renderContent()}
        </div>
      </main>
      <ModalHost modal={modal} setModal={setModal} state={current} notify={notify} refresh={refresh} deleteSkill={deleteSkill} runTask={runTask} />
      <div class={`toast ${toast ? 'show' : ''}`} role="status">{toast}</div>
    </div>
  );
}