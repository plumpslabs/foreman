import { useEffect, useRef, useState } from 'preact/hooks';
import { api, loadWorkspace } from './api.js';
import { HomePage, RoomsPage, RoomDetailHost, TeamsPage, AgentsPage, SkillsPage, RunsPage, SettingsPage } from './pages.jsx';
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
const NAV_SECONDARY = [['settings', 'Settings', 'settings-2']];

const PARENT_PAGE = {
  'room-detail': 'rooms',
  'agent-detail': 'agents',
  'team-detail': 'teams',
  'team-create': 'teams',
  'skill-detail': 'skills',
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
  const [state, setState] = useState(null);
  const [page, setPage] = useState('home');
  const [selectedId, setSelectedId] = useState('');
  const [modal, setModal] = useState(null);
  const [toast, setToast] = useState('');
  const toastTimer = useRef(0);
  const [sidebarOpen, setSidebarOpen] = useState(false);

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

  const current = state || emptyState;
  const settings = current.settings || emptyState.settings;
  const themeClass = settings.theme === 'light' ? 'theme-light' : '';

  const navigate = (next) => {
    setSelectedId('');
    setPage(next);
    setSidebarOpen(false);
  };

  const openRoom = (id) => { setSelectedId(id); setPage('room-detail'); };
  const openAgent = (id) => { setSelectedId(id); setPage('agent-detail'); };
  const openTeam = (id) => { setSelectedId(id); setPage('team-detail'); };
  const openSkill = (id) => { setSelectedId(id); setPage('skill-detail'); };
  const openTeamCreate = () => { setSelectedId(''); setPage('team-create'); };

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

  const cancelRun = async (runId) => {
    try {
      await api(`/api/runs/${encodeURIComponent(runId)}/cancel`, { method: 'POST', body: '{}' });
      notify('Cancellation requested');
      await refresh();
    } catch (error) {
      notify(error.message);
    }
  };

  const pageProps = {
    state: current, notify, refresh, setModal, navigate,
    openRoom, openAgent, openTeam, openSkill, openTeamCreate,
    runTask, deleteSkill, cancelRun,
  };

  const pages = {
    home: <HomePage {...pageProps} />,
    rooms: <RoomsPage {...pageProps} />,
    teams: <TeamsPage {...pageProps} />,
    agents: <AgentsPage {...pageProps} />,
    skills: <SkillsPage {...pageProps} />,
    runs: <RunsPage {...pageProps} />,
    settings: <SettingsPage {...pageProps} />,
  };

  const activeRoom = current.rooms.find((room) => room.id === selectedId);
  const activeAgent = (current.agents || []).find((agent) => agent.id === selectedId);
  const activeTeam = current.teams.find((team) => team.id === selectedId);
  const activeSkill = (current.skills || []).find((skill) => skill.id === selectedId);

  const parent = PARENT_PAGE[page] || page;
  const pageLabel = parent === 'home' ? 'Overview'
    : (NAV_PRIMARY.concat(NAV_SECONDARY).find(([key]) => key === parent) || ['home', 'Overview'])[1];

  const renderNav = (items) => items.map(([key, label, icon]) => (
    <button class={`nav-btn ${parent === key ? 'active' : ''}`} onClick={() => navigate(key)}>
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
          ? <TeamDetailPage key={`team-${selectedId}`} teamId={selectedId} {...pageProps} onBack={() => navigate('teams')} />
          : <TeamsPage {...pageProps} />;
      case 'team-create':
        return (
          <TeamCreatePage
            {...pageProps}
            onBack={() => navigate('teams')}
            onCreated={(id) => openTeam(id)}
          />
        );
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
    <div class={`app ${themeClass}`}>
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
            <button class="icon-btn mobile-menu" onClick={() => setSidebarOpen((v) => !v)} aria-label="Toggle menu">
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
            <div class="status-pill"><span class="status-dot" />Runtime ready</div>
            <button class="icon-btn" aria-label="Search"><Icon name="search" /></button>
            <button class="icon-btn" aria-label="Notifications"><Icon name="bell" /></button>
            <div class="avatar">MW</div>
          </div>
        </header>
        <div class={`content ${['room-detail', 'agent-detail', 'team-detail', 'team-create', 'skill-detail'].includes(page) ? 'flush' : ''}`}>
          {renderContent()}
        </div>
      </main>
      <ModalHost modal={modal} setModal={setModal} state={current} notify={notify} refresh={refresh} deleteSkill={deleteSkill} />
      <div class={`toast ${toast ? 'show' : ''}`} role="status">{toast}</div>
    </div>
  );
}