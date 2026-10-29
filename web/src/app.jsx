import { useEffect, useRef, useState } from 'preact/hooks';
import { api, loadWorkspace } from './api.js';
import { HomePage, RoomsPage, RoomDetailHost, TeamsPage, KanbanPage, AgentsPage, SkillsPage, RunsPage, SettingsPage } from './pages.jsx';
import { ModalHost } from './modals.jsx';

const NAV_PRIMARY = [
  ['home', 'Home'],
  ['rooms', 'Rooms'],
  ['kanban', 'Kanban'],
  ['teams', 'Teams'],
  ['agents', 'Agents'],
  ['skills', 'Skills'],
  ['runs', 'Runs'],
];
const NAV_SECONDARY = [['settings', 'Settings']];

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
  const [modal, setModal] = useState(null); // {kind, roomId?, templateId?}
  const [toast, setToast] = useState('');
  const toastTimer = useRef(0);

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
  };

  const runTask = async (taskId, button) => {
    if (button) button.disabled = true;
    try {
      await api(`/api/tasks/${encodeURIComponent(taskId)}/run`, { method: 'POST', body: '{}' });
      notify('Run started');
      await refresh();
      navigate('runs');
    } catch (error) {
      notify(error.message);
    } finally {
      if (button) button.disabled = false;
    }
  };

  const setTaskStatus = async (taskId, status, approved = true) => {
    try {
      await api(`/api/tasks/${encodeURIComponent(taskId)}/status`, {
        method: 'PUT',
        body: JSON.stringify({ status, approved }),
      });
      await refresh();
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

  const deleteSkill = async (skillId) => {
    try {
      await api(`/api/skills/${encodeURIComponent(skillId)}`, { method: 'DELETE' });
      await refresh();
      notify('Skill deleted');
    } catch (error) {
      notify(error.message);
    }
  };

  const openRoom = (roomId) => {
    setActiveRoomId(roomId);
    setPage('rooms');
  };

  const pageProps = {
    state: current,
    notify,
    refresh,
    openRoom,
    setModal,
    runTask,
    setTaskStatus,
    cancelRun,
    deleteSkill,
    activeRoomId,
    setActiveRoomId,
  };

  const pages = {
    home: <HomePage {...pageProps} />,
    rooms: <RoomsPage {...pageProps} />,
    teams: <TeamsPage {...pageProps} />,
    kanban: <KanbanPage {...pageProps} />,
    agents: <AgentsPage {...pageProps} />,
    skills: <SkillsPage {...pageProps} />,
    runs: <RunsPage {...pageProps} />,
    settings: <SettingsPage {...pageProps} />,
  };

  const renderNav = (items) => items.map(([key, label]) => (
    <button class={page === key ? 'active' : ''} onClick={() => navigate(key)}>{label}</button>
  ));

  const activeRoom = current.rooms.find((room) => room.id === activeRoomId);
  const pageLabel = (NAV_PRIMARY.concat(NAV_SECONDARY).find(([key]) => key === page) || ['home'])[1];

  return (
    <div class={`app ${themeClass}`}>
      <aside class="sidebar">
        <div class="brand">
          <div class="mark">F</div>
          <div>
            <strong>foreman.</strong>
            <small>Local workspace</small>
          </div>
        </div>
        <nav class="nav" aria-label="Main navigation">
          <div class="nav-label">Workspace</div>
          {renderNav(NAV_PRIMARY)}
          <div class="nav-label">Configure</div>
          {renderNav(NAV_SECONDARY)}
        </nav>
        <div class="workspace-path">{current.projectPath || 'Loading project…'}</div>
      </aside>
      <main class="main">
        <header class="topbar">
          <div>Workspace <span aria-hidden="true">/</span> <b>{pageLabel}</b></div>
          <span class="runtime">Local service ready</span>
        </header>
        <div class="content">
          {state === null
            ? <div class="empty">Loading workspace…</div>
            : (activeRoom && page === 'rooms'
                ? <RoomDetailHost room={activeRoom} {...pageProps} />
                : pages[page])}
        </div>
      </main>
      <ModalHost
        modal={modal}
        setModal={setModal}
        state={current}
        notify={notify}
        refresh={refresh}
      />
      <div class={`toast ${toast ? 'show' : ''}`} role="status">{toast}</div>
    </div>
  );
}
