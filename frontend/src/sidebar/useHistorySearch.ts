import {useEffect, useRef, useState} from 'react';
import {SearchConversations} from '../../wailsjs/go/main/App';
import {main} from '../../wailsjs/go/models';
import {asArray, formatError} from '../utils';

// useHistorySearch owns the sidebar's full-history search: the query, the
// search field's open state, and a debounced SearchConversations call whose
// sequence ref drops a slow earlier response once the query has moved on.
function useHistorySearch() {
  // historyQuery drives the sidebar's full-history search; results replace
  // the conversation list while non-empty.
  const [historyQuery, setHistoryQuery] = useState('');
  // searchOpen reveals the sidebar search field; it collapses back to the icon
  // only when both closed by the user and the query is empty.
  const [searchOpen, setSearchOpen] = useState(false);

  const [historyResults, setHistoryResults] = useState<main.ConversationSearchResult[]>([]);
  const [historySearchBusy, setHistorySearchBusy] = useState(false);
  const [historySearchError, setHistorySearchError] = useState('');
  const [historySearchTruncated, setHistorySearchTruncated] = useState(false);
  const historySearchSeqRef = useRef(0);

  // Debounced sidebar search over full conversation history. The sequence
  // ref lets a slow earlier response be discarded when the query changes
  // again before it lands.
  useEffect(() => {
    const seq = ++historySearchSeqRef.current;
    const query = historyQuery.trim();
    if (!query) {
      setHistoryResults([]);
      setHistorySearchError('');
      setHistorySearchTruncated(false);
      setHistorySearchBusy(false);
      return;
    }
    setHistorySearchBusy(true);
    const timer = window.setTimeout(() => {
      SearchConversations(query, new main.SearchOptions())
        .then((response) => {
          if (seq !== historySearchSeqRef.current) {
            return;
          }
          setHistoryResults(asArray(response?.results));
          setHistorySearchTruncated(Boolean(response?.truncated));
          setHistorySearchError('');
        })
        .catch((error) => {
          if (seq !== historySearchSeqRef.current) {
            return;
          }
          setHistoryResults([]);
          setHistorySearchTruncated(false);
          setHistorySearchError(formatError(error));
        })
        .finally(() => {
          if (seq === historySearchSeqRef.current) {
            setHistorySearchBusy(false);
          }
        });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [historyQuery]);

  return {
    historyQuery,
    historyResults,
    historySearchBusy,
    historySearchError,
    historySearchTruncated,
    searchOpen,
    setHistoryQuery,
    setSearchOpen,
  };
}

export {useHistorySearch};
