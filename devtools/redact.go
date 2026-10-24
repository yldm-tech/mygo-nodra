package devtools

// Run application callbacks before taking Inspector.mu; redactors may inspect the inspector. Never retain callback errors, which may themselves contain sensitive input.
func (i *Inspector) redactEvent(event Event) Event {
	if i.options.RedactMetadata == nil {
		return event
	}
	metadata := i.redactMetadata(event.StoreID, Metadata{Name: event.Name, Key: event.Key, Error: event.Error})
	event.Name, event.Key, event.Error = metadata.Name, metadata.Key, metadata.Error
	return event
}

func (i *Inspector) redactMetadata(id string, original Metadata) (result Metadata) {
	result = Metadata{Error: "metadata redaction failed"}
	defer func() {
		if recover() != nil {
			result = Metadata{Error: "metadata redaction failed"}
		}
	}()
	if filtered, err := i.options.RedactMetadata(id, original); err == nil {
		result = filtered
	}
	return result
}
