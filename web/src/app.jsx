import { useEffect, useRef, useState } from 'preact/hooks';
import { api, loadWorkspace } from './api.js';
import { HomePage, RoomsPage, RoomDetailHost, TeamsPage, AgentsPage, SkillsPage, RunsPage, SettingsPage } from './pages.jsx';
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

const emptyState = {
  projectPath: '',
  rooms: [],
  teams: [],
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
  const [activeRoomId, setActiveRoomId] = useState('');
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
    setActiveRoomId('');
    setPage(next);
    setSidebarOpen(false);
  };

  const openRoom = (roomId) => {
    setActiveRoomId(roomId);
    setPage('room-detail');
  };

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

  const saveSkill = async (skill, message) => {
    try {
      await api('/api/skills', { method: 'POST', body: JSON.stringify(skill) });
      await refresh();
      if (message) notify(message);
      return true;
    } catch (error) {
      notify(error.message);
      return false;
    }
  };

  const deleteAgent = async (agentId) => {
    try {
      await api(`/api/agents/${encodeURIComponent(agentId)}`, { method: 'DELETE' });
      await refresh();
      notify('Agent deleted');
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
    state: current, notify, refresh, openRoom, setModal,
    runTask, deleteSkill, saveSkill, deleteAgent, cancelRun, activeRoomId, setActiveRoomId,
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

  const renderNav = (items) => items.map(([key, label, icon]) => (
    <button class={`nav-btn ${page === key ? 'active' : ''}`} onClick={() => navigate(key)}>
      <Icon name={icon} />
      <span class="nav-label-text">{label}</span>
    </button>
  ));

  const activeRoom = current.rooms.find((room) => room.id === activeRoomId);
  const pageLabel = page === 'room-detail' ? (activeRoom ? activeRoom.name : 'Room') :
    (NAV_PRIMARY.concat(NAV_SECONDARY).find(([key]) => key === page) || ['home'])[1];

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
              <span>Workspace</span>
              <Icon name="chevron-right" size={13} />
              <b>{pageLabel}</b>
            </div>
          </div>
          <div class="top-actions">
            <div class="status-pill"><span class="status-dot" />Runtime ready</div>
            <button class="icon-btn" aria-label="Search"><Icon name="search" /></button>
            <button class="icon-btn" aria-label="Notifications"><Icon name="bell" /></button>
            <div class="avatar">MW</div>
          </div>
        </header>
        <div class={`content ${page === 'room-detail' ? 'flush' : ''}`}>
          {state === null
            ? <div class="empty">Loading workspace…</div>
            : page === 'room-detail'
              ? activeRoom ? <RoomDetailHost room={activeRoom} {...pageProps} /> : <div class="empty">Room not found</div>
              : pages[page]}
        </div>
      </main>
      <ModalHost modal={modal} setModal={setModal} state={current} notify={notify} refresh={refresh}
        saveSkill={saveSkill} deleteSkill={deleteSkill} deleteAgent={deleteAgent} />
      <div class={`toast ${toast ? 'show' : ''}`} role="status">{toast}</div>
    </div>
  );
}
