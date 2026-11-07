import {ModelSelectionPanel} from '../settings/ModelSelectionPanel';
import {AppModel} from '../app/useAppModel';

export function ConversationModelsView({app}: {app: AppModel}) {
  return (
    <>
            <>
              <div className="toolbar">
                <div className="toolbar-left">
                  <button className="back-button" onClick={app.closeConversationModels}>← Back</button>
                  <div className="model-count">
                    {app.convModelsKeys.size
                      ? `${app.convModelsKeys.size} override${app.convModelsKeys.size === 1 ? '' : 's'} ${app.activeConversationID ? 'for this conversation' : 'for your next conversation'}`
                      : 'Following the Settings → Models defaults'}
                  </div>
                </div>
                {app.convModelsKeys.size ? (
                  <div className="toolbar-right">
                    <button className="back-button" onClick={app.resetAllConversationModels}>Reset all</button>
                  </div>
                ) : null}
              </div>
              <div className="settings-screen conversation-models-screen">
                <div className="settings-header">
                  <h2>{app.activeConversationID ? 'Models for this conversation' : 'Models for your next conversation'}</h2>
                  {app.activeConversationID ? (
                    <p>
                      Changes here apply only to “{app.activeConversation?.title || 'this conversation'}”. Everything you
                      leave alone keeps following the Settings → Models defaults, and later Settings changes reach those
                      fields too.
                    </p>
                  ) : (
                    <p>
                      Changes here apply to the conversation you start next — from its very first message. Everything you
                      leave alone keeps following the Settings → Models defaults.
                    </p>
                  )}
                </div>
                <ModelSelectionPanel
                  variant="conversation"
                  value={app.convModelsDraft ?? app.conversationModelSelection}
                  onChange={app.patchConversationModels}
                  catalogs={app.modelSelectionCatalogs}
                  durationOptions={(() => {
                    const withCurrent = (options: string[], current: string) => (options.includes(current) ? options : [current, ...options]);
                    const shown = app.convModelsDraft ?? app.conversationModelSelection;
                    return {
                      video: withCurrent(app.convDurationOptions.video, shown.videoDuration),
                      image: app.convDurationOptions.image,
                      keyframe: app.convDurationOptions.keyframe,
                      extend: app.convDurationOptions.extend,
                      sound: withCurrent(app.convDurationOptions.sound, shown.soundDuration),
                    };
                  })()}
                  overriddenKeys={app.convModelsKeys}
                  onResetField={app.resetConversationModelField}
                  airplaneMode={app.airplaneMode}
                />
              </div>
            </>
    </>
  );
}
