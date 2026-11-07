import {UsageReportView} from '../usage/UsageReportView';
import {AppModel} from '../app/useAppModel';

export function UsageView({app}: {app: AppModel}) {
  return (
    <>
            <UsageReportView
              report={app.usageReport}
              loading={app.usageLoading}
              error={app.usageError}
              onBack={() => app.setView('app')}
              onOpenChild={(child) => {
                // A library report's children are projects, a project report's
                // are conversations — the child scope follows the parent's.
                if (app.usageReport?.scope === 'library') {
                  app.openProjectUsageReport(child.id);
                } else {
                  app.openConversationUsageReport(child.id);
                }
              }}
            />
    </>
  );
}
