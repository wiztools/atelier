import {useEffect, useState} from 'react';

// useOverlays owns the two transient overlays any screen can raise: the
// image preview lightbox (the URL it shows; empty when closed) and the model
// capability popover (the id of the picker whose popover is open). Escape
// closes either; an outside press closes the popover.
function useOverlays() {
  const [previewImage, setPreviewImage] = useState('');
  const [openCapabilityID, setOpenCapabilityID] = useState('');

  useEffect(() => {
    if (!previewImage) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setPreviewImage('');
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [previewImage]);

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
    setOpenCapabilityID,
    setPreviewImage,
  };
}

export {useOverlays};
