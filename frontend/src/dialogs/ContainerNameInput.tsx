// ContainerNameInput is the shared inline input for creating or renaming a
// library/project: Enter submits, Escape cancels, blur submits (clicking
// elsewhere commits the edit). The four tree call sites differ only in value,
// placeholder, and what submit/cancel mean.
function ContainerNameInput(props: {
  value: string;
  placeholder?: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  onCancel: () => void;
}) {
  return (
    <input
      className="container-name-input"
      autoFocus
      value={props.value}
      placeholder={props.placeholder}
      onChange={(event) => props.onChange(event.target.value)}
      onKeyDown={(event) => {
        if (event.key === 'Enter') {
          event.preventDefault();
          props.onSubmit();
        }
        if (event.key === 'Escape') {
          event.preventDefault();
          props.onCancel();
        }
      }}
      onBlur={props.onSubmit}
    />
  );
}

export {ContainerNameInput};
