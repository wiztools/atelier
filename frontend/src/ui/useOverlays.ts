import {useEffect, useState} from 'react';

// previewMedia is the lightbox's moving-picture sibling: a video or audio URL
// shown in the same dialog shell (the editor media pickers' Preview buttons
// raise it). The two previews never coexist — each setter clears the other.
type PreviewMedia = {url: string; kind: 'video' | 'audio'};

// useOverlays owns the two transient overlays any screen can raise: the
// image preview lightbox (the URL it shows; empty when closed) and the model
// capability popover (the id of the picker whose popover is open). Escape
// closes either; an outside press closes the popover.
function useOverlays() {
  const [previewImage, setPreviewImageState] = useState('');
  const [previewMedia, setPreviewMediaState] = useState<PreviewMedia | null>(null);
  const [openCapabilityID, setOpenCapabilityID] = useState('');

  // The cross-clears live in wrappers so callers keep their one-argument
  // signatures: every existing image call site (assets panel, chat chips,
  // overlay close buttons) is untouched, and the media setters the editor
  // pickers use can never leave a stale image behind the video.
  const setPreviewImage = (url: string) => {
    setPreviewImageState(url);
    setPreviewMediaState(null);
  };
  const setPreviewMedia = (url: string, kind: 'video' | 'audio') => {
    setPreviewMediaState({url, kind});
    setPreviewImageState('');
  };
  const closePreviews = () => {
    setPreviewImageState('');
    setPreviewMediaState(null);
  };

  useEffect(() => {
    if (!previewImage && !previewMedia) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        closePreviews();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [previewImage, previewMedia]);

  useEffect(() => {
    if (!openCapabilityID) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (event.target instanceof Element && event.target.closest('.model-capability')) {
        return;
      }
      setOpenCapabilityID('');
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpenCapabilityID('');
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [openCapabilityID]);

  return {
    openCapabilityID,
    previewImage,
    previewMedia,
    closePreviews,
    setOpenCapabilityID,
    setPreviewImage,
    setPreviewMedia,
  };
}

export {useOverlays};
