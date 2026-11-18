import "./attempt_edit_warning_note.scss";

export default function AttemptEditWarning({ message }: { message: string }) {
  return (
    <p role="note" className="attempt-edit-warning">
      {message}
    </p>
  );
}
