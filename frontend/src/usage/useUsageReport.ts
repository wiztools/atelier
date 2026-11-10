import {useState} from 'react';
import {
  GetConversationUsageReport,
  GetLibraryUsageReport,
  GetProjectUsageReport,
  SaveUsageReportExport,
} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {trackSave} from '../notifications';
import {View} from '../types';

type UsageReportExportFormat = 'pdf' | 'json';

// usageReportScopeID is the id the report was folded for — the one its own
// scope names.
function usageReportScopeID(report: main.UsageReport): string {
  switch (report.scope) {
    case 'library':
      return report.libraryId ?? '';
    case 'project':
      return report.projectId ?? '';
    default:
      return report.conversationId ?? '';
  }
}

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
  // Export re-folds the shown scope on the Go side (the report is never
  // cached, so the file matches the history at the moment of the click) and
  // opens the save dialog; trackSave reports the saved file or the failure.
  const [usageExporting, setUsageExporting] = useState<UsageReportExportFormat | null>(null);
  const exportUsageReport = (format: UsageReportExportFormat) => {
    if (!usageReport || usageExporting) {
      return;
    }
    setUsageExporting(format);
    void trackSave(
      format === 'pdf' ? 'usage report PDF' : 'usage report JSON',
      SaveUsageReportExport(main.SaveUsageReportExportRequest.createFrom({
        scope: usageReport.scope,
        id: usageReportScopeID(usageReport),
        format,
      })),
    ).finally(() => setUsageExporting(null));
  };

  return {
    exportUsageReport,
    openConversationUsageReport,
    openLibraryUsageReport,
    openProjectUsageReport,
    usageError,
    usageExporting,
    usageLoading,
    usageReport,
  };
}

export {useUsageReport};
export type {UsageReportExportFormat};
