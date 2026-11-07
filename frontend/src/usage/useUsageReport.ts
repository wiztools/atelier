import {useState} from 'react';
import {GetConversationUsageReport, GetLibraryUsageReport, GetProjectUsageReport} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {View} from '../types';

// useUsageReport owns the usage report screen's state — the report being
// shown, its loading and error flags — and the open functions for each scope
// (conversation, project, library), which switch App's view to the report and
// fetch it.
function useUsageReport({setView}: {
  setView: React.Dispatch<React.SetStateAction<View>>;
}) {
  // Usage report: the aggregated cost + tool-call view (view === 'usage'),
  // opened at conversation, project, or library scope from the sidebar menus
  // and the chat toolbar. The report is derived on demand — nothing is
  // cached, so a drill-down always reflects the history as it stands now.
  const [usageReport, setUsageReport] = useState<main.UsageReport | null>(null);
  const [usageLoading, setUsageLoading] = useState(false);
  const [usageError, setUsageError] = useState('');
  const openUsageReport = (fetcher: () => Promise<main.UsageReport>) => {
    setUsageError('');
    setUsageLoading(true);
    setView('usage');
    fetcher()
      .then((report) => setUsageReport(report))
      .catch((err) => {
        setUsageReport(null);
        setUsageError(String(err));
      })
      .finally(() => setUsageLoading(false));
  };
  const openConversationUsageReport = (conversationID: string) => {
    openUsageReport(() => GetConversationUsageReport(conversationID));
  };
  const openProjectUsageReport = (projectID: string) => {
    openUsageReport(() => GetProjectUsageReport(projectID));
  };
  const openLibraryUsageReport = (libraryID: string) => {
    openUsageReport(() => GetLibraryUsageReport(libraryID));
  };

  return {
    openConversationUsageReport,
    openLibraryUsageReport,
    openProjectUsageReport,
    usageError,
    usageLoading,
    usageReport,
  };
}

export {useUsageReport};
